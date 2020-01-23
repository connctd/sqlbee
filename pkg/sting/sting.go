package sting

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/howeyc/fsnotify"
	"github.com/mattbaird/jsonpatch"
	"github.com/sirupsen/logrus"

	"k8s.io/api/admission/v1beta1"
	admissionregistrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	appsv1beta1 "k8s.io/api/apps/v1beta1"
	appsv1beta2 "k8s.io/api/apps/v1beta2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	k8sjson "k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/kubernetes/pkg/apis/core/v1"
)

var Version = "unset"

var (
	// WrongResourceError can be used to indicate that this webhook doesn't support this resource-
	// This might happen due to wrong configuration etc.
	WrongResourceError = errors.New("Wrong resource type")
)

// Schemas, codecs etc. so serialize and deserialize datatypes for Kubernetes
var (
	RuntimeScheme = runtime.NewScheme()
	Codecs        = serializer.NewCodecFactory(RuntimeScheme)
	Deserializer  = Codecs.UniversalDeserializer()
	Marshaler     = k8sjson.NewSerializer(k8sjson.DefaultMetaFactory, RuntimeScheme, RuntimeScheme, false)

	// (https://github.com/kubernetes/kubernetes/issues/57982)
	Defaulter = runtime.ObjectDefaulter(RuntimeScheme)
)

// There are some things, which might get added by a patch, which we don't care about or are
// generated/overwritten by the kubernetes master anyway
var ignoredPatchPaths = []string{"/spec/template/metadata/creationTimestamp", "/status",
	"/metadata/creationTimestamp"}

func init() {
	_ = corev1.AddToScheme(RuntimeScheme)
	_ = appsv1.AddToScheme(RuntimeScheme)
	_ = appsv1beta1.AddToScheme(RuntimeScheme)
	_ = appsv1beta2.AddToScheme(RuntimeScheme)
	_ = admissionregistrationv1beta1.AddToScheme(RuntimeScheme)
	// defaulting with webhooks:
	// https://github.com/kubernetes/kubernetes/issues/57982
	_ = v1.AddToScheme(RuntimeScheme)
}

// MutateFunc is the definition for functions doing the mutation of a resource
type MutateFunc func(ar *v1beta1.AdmissionReview) *v1beta1.AdmissionResponse

// NeedsMutationFunc can be used to run more complex checks before MutateFunc is called
type NeedsMutationFunc func(ar *v1beta1.AdmissionReview) bool

// IsAdmittedFunc is used for admitting only webhooks to determine wether a resource can be admitted
type IsAdmittedFunc func(ar *v1beta1.AdmissionReview) (*v1beta1.AdmissionResponse, error)

// InjectServer is an opinionated implementation of a service running within kubernetes as admission
// webhook. It provides a HTTPS secured endpoint for admission/mutation and a HTTP endpoint for
// readiness and liveness checks
type InjectServer struct {
	server      *http.Server
	cert        *tls.Certificate
	certLock    *sync.Mutex
	adminServer *http.Server

	mutate      MutateFunc
	needsMutate NeedsMutationFunc
	isAdmitted  IsAdmittedFunc
}

// Options are used to configure the InjectServer
type Options struct {
	// ListenAddr is used for the admission endpoint. Default is :443
	ListenAddr string
	// The function implementation to be used when running mutations.
	Mutate MutateFunc
	// Optional function to be used to decide whether a mutation is necessary or not
	NeedsMutate NeedsMutationFunc
	// IsAdmitted can be set to enable admission checks
	IsAdmitted IsAdmittedFunc

	// These are parameters for the HTTP(S) server, they are optional and default to sane values
	ReadTimeout       time.Duration
	IdleTimeout       time.Duration
	ReadHeaderTimeout time.Duration
	WriteTimeout      time.Duration

	// Path to the server X.509 certificate
	CertFile string
	// Path to the server private key
	KeyFile string
	// Unused so far. Will be required for support of TLS authenticated clients
	CaFile string

	// The amount of CPU to be requested
	cpuRequest string
	// The amount of memory to be requested
	memRequest string
}

// Main is a simple helper method which takes an io.Closer and blocks until either
// SIGTERM oder SIGINT are received and the calls Close() in the io.Closer() and exits with
// (0)
func Main(closeable io.Closer) {
	var gracefulStop = make(chan os.Signal)
	signal.Notify(gracefulStop, syscall.SIGTERM)
	signal.Notify(gracefulStop, syscall.SIGINT)

	<-gracefulStop
	if err := closeable.Close(); err != nil {
		logrus.WithError(err).Error("Failed to shutdown closeable")
	}
	os.Exit(0)
}

// NewOptions creates a new instance of an Options struct with sane values set. Only
// Mutate, NeedsMutate or IsAdmitted need to set now.
func NewOptions() *Options {
	return &Options{
		ListenAddr:        ":443",
		ReadTimeout:       time.Second * 10,
		IdleTimeout:       time.Second * 10,
		ReadHeaderTimeout: time.Second * 2,
		WriteTimeout:      time.Second * 10,
	}
}

// New creates and starts a new InjectServer. InjectServer implements io.Closer
// so it can be used together with the helper function Main
func New(opts *Options) (*InjectServer, error) {
	i := &InjectServer{
		mutate:      opts.Mutate,
		needsMutate: opts.NeedsMutate,
		isAdmitted:  opts.IsAdmitted,
		certLock:    &sync.Mutex{},
	}

	pair, err := tls.LoadX509KeyPair(opts.CertFile, opts.KeyFile)
	if err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"certPath": opts.CertFile,
			"keyPath":  opts.KeyFile,
		}).Error("Failed to load TLS X.509 keypair")
		return nil, err
	}
	i.cert = &pair

	r := mux.NewRouter()
	r.Use(validateContentType("application/json"))

	if opts.Mutate != nil {
		logrus.WithField("urlPath", "/api/v1beta/mutate").Info("Adding mutating admission endpoint")
		r.Path("/api/v1beta/mutate").Methods(http.MethodPost).HandlerFunc(i.handleMutate)
	}

	if opts.IsAdmitted != nil {
		logrus.WithField("urlPath", "/api/v1beta/admit").Info("Adding non mutating admission endpoint")
		r.Path("/api/v1beta/admit").Methods(http.MethodPost).HandlerFunc(i.handleAdmission)
	}

	ar := mux.NewRouter()
	ar.Path("/health").Methods(http.MethodGet).HandlerFunc(i.healtHandler)

	i.server = &http.Server{
		Addr:              opts.ListenAddr,
		Handler:           r,
		ReadTimeout:       opts.ReadTimeout,
		IdleTimeout:       opts.IdleTimeout,
		ReadHeaderTimeout: opts.ReadHeaderTimeout,
		WriteTimeout:      opts.WriteTimeout,
		TLSConfig: &tls.Config{
			GetCertificate: i.getCert,
		},
		// TODO k8s compatible TLS config
	}

	i.adminServer = &http.Server{
		Addr:              ":8080",
		Handler:           ar,
		ReadTimeout:       opts.ReadTimeout,
		IdleTimeout:       opts.IdleTimeout,
		ReadHeaderTimeout: opts.ReadHeaderTimeout,
		WriteTimeout:      opts.WriteTimeout,
	}

	certWatcher, err := fsnotify.NewWatcher()
	if err := certWatcher.Watch(opts.CertFile); err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"certPath": opts.CertFile,
		}).Error("Failed to creat file watcher for certificate")
		return nil, err
	}

	go func(watcher *fsnotify.Watcher, opts *Options) {
		for {
			select {
			case ev := <-watcher.Event:
				if ev.IsModify() || ev.IsCreate() {
					logrus.WithFields(logrus.Fields{
						"certPath": opts.CertFile,
						"keyPath":  opts.KeyFile,
					}).Info("Certificate has been updated reloading keypair")
					pair, err := tls.LoadX509KeyPair(opts.CertFile, opts.KeyFile)
					if err == nil {
						i.certLock.Lock()
						i.cert = &pair
						i.certLock.Unlock()
					} else {
						logrus.WithError(err).WithFields(logrus.Fields{
							"certPath": opts.CertFile,
							"keyPath":  opts.KeyFile,
						}).Panic("Failed to reload keypair!")
					}
				}
			}
		}
	}(certWatcher, opts)

	go func() {
		logrus.WithFields(logrus.Fields{
			"listenAddr": opts.ListenAddr,
		}).Info("HTTPS server listening")
		if err := i.server.ListenAndServeTLS("", ""); err != nil {
			logrus.WithError(err).Error("Failed to listen as TLS server")
		}
	}()

	go func() {
		logrus.WithFields(logrus.Fields{
			"listenAddr": i.adminServer.Addr,
		}).Info("Liveness and readiness HTTP server listening")
		if err := i.adminServer.ListenAndServe(); err != nil {
			logrus.WithError(err).Error("Liveness and readiness HTTP server failed to listen")
		}
	}()

	return i, nil
}

// Close is necessary to implement io.Closer interface
func (i *InjectServer) Close() error {
	logrus.WithFields(logrus.Fields{
		"timeOut":    "15s",
		"listenAddr": i.server.Addr,
	}).Info("Shutting down HTTPS server")
	shutdownCtx, _ := context.WithTimeout(context.Background(), 15*time.Second)
	go i.server.Shutdown(shutdownCtx)
	go i.adminServer.Shutdown(shutdownCtx)
	<-shutdownCtx.Done()
	return nil
}

func (i *InjectServer) getCert(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	i.certLock.Lock()
	defer i.certLock.Unlock()
	return i.cert, nil
}

func (i *InjectServer) healtHandler(w http.ResponseWriter, r *http.Request) {

}

func readRequest(w http.ResponseWriter, r *http.Request) (*v1beta1.AdmissionReview, error) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"remoteAddr": r.RemoteAddr,
			"requestUri": r.RequestURI,
			"protocol":   r.Proto,
		}).Error("Failed to read request body from client")
		err := fmt.Errorf("Failed to read request body")
		errorResponse(err, http.StatusBadRequest, nil, w)
		return nil, fmt.Errorf("Failed to read request body")
	}

	if len(body) == 0 {
		err := fmt.Errorf("Request body is empty")
		logrus.WithError(err).WithFields(logrus.Fields{
			"remoteAddr": r.RemoteAddr,
			"requestUri": r.RequestURI,
			"protocol":   r.Proto,
		}).Error("Failed to read request body from client")
		errorResponse(err, http.StatusBadRequest, nil, w)
		return nil, err
	}

	logrus.WithFields(logrus.Fields{
		"remoteAddr": r.RemoteAddr,
		"requestUri": r.RequestURI,
		"protocol":   r.Proto,
		"body":       string(body),
		"bodyLength": len(body),
	}).Debug("Received request body")

	ar := v1beta1.AdmissionReview{}
	if err := json.Unmarshal(body, &ar); err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"remoteAddr": r.RemoteAddr,
			"requestUri": r.RequestURI,
			"protocol":   r.Proto,
			"body":       string(body),
		}).Error("Failed to unmarshal request body")
		errorResponse(err, http.StatusBadRequest, &ar, w)
		return nil, err
	}
	return &ar, nil
}

func (i *InjectServer) handleMutate(w http.ResponseWriter, r *http.Request) {
	if i.mutate == nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		logrus.Panic("Mutate function not set and handleMuate called, this shouldn't happen!")
		return
	}

	ar, err := readRequest(w, r)
	if err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"remoteAddr": r.RemoteAddr,
			"requestUri": r.RequestURI,
			"protocol":   r.Proto,
		}).Error("Failed to read request")
		return
	}

	var admissionResponse *v1beta1.AdmissionResponse
	response := v1beta1.AdmissionReview{}

	if i.needsMutate != nil && !i.needsMutate(ar) {
		logrus.WithFields(logrus.Fields{
			"name":         ar.Request.Name,
			"namespace":    ar.Request.Namespace,
			"groupVersion": ar.Request.Resource.String(),
			"requestUID":   ar.Request.UID,
		}).Info("This resource doesn't need mutation, allowing the request")
		admissionResponse = &v1beta1.AdmissionResponse{}
		admissionResponse.Allowed = true
		admissionResponse.Result = &metav1.Status{Message: "This resource does not need mutation"}
		response.Response = admissionResponse
	} else {
		logrus.WithFields(logrus.Fields{
			"name":         ar.Request.Name,
			"namespace":    ar.Request.Namespace,
			"groupVersion": ar.Request.Resource.String(),
			"requestUID":   ar.Request.UID,
		}).Info("Mutating resource")
		admissionResponse = i.mutate(ar)
		if admissionResponse == nil {
			logrus.WithFields(logrus.Fields{
				"name":         ar.Request.Name,
				"namespace":    ar.Request.Namespace,
				"groupVersion": ar.Request.Resource.String(),
				"requestUID":   ar.Request.UID,
			}).Error("Admission response was nil, some error occured")
			errorResponse(fmt.Errorf("Failed to generate admission response"), http.StatusInternalServerError, ar, w)
			return
		}
	}

	response.Response = admissionResponse
	if ar.Request != nil && response.Response != nil {
		response.Response.UID = ar.Request.UID
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"name":         ar.Request.Name,
			"namespace":    ar.Request.Namespace,
			"groupVersion": ar.Request.Resource.String(),
			"requestUID":   ar.Request.UID,
		}).Error("Failed to serialize admission response to JSON")
	}
}

func (i *InjectServer) handleAdmission(w http.ResponseWriter, r *http.Request) {
	if i.isAdmitted == nil {
		logrus.Panic("isAdmitted function not set and handleAdmission called, this shouldn't happen!")
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}

	ar, err := readRequest(w, r)
	if err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"remoteAddr": r.RemoteAddr,
			"requestUri": r.RequestURI,
			"protocol":   r.Proto,
		}).Error("Failed to read request")
		return
	}

	admissionResponse, err := i.isAdmitted(ar)
	if err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"remoteAddr":   r.RemoteAddr,
			"requestUri":   r.RequestURI,
			"protocol":     r.Proto,
			"name":         ar.Request.Name,
			"namespace":    ar.Request.Namespace,
			"groupVersion": ar.Request.Resource.String(),
			"requestUID":   ar.Request.UID,
		}).Error("An error occured during admission decision")
		errorResponse(err, http.StatusNotAcceptable, ar, w)
		return
	}
	response := v1beta1.AdmissionReview{}
	response.Response = admissionResponse
	if ar.Request != nil {
		response.Response.UID = ar.Request.UID
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"requestUID":   ar.Request.UID,
			"name":         ar.Request.Name,
			"namespace":    ar.Request.Namespace,
			"groupVersion": ar.Request.Resource.String(),
		}).Error("Failed to serialize admission response to JSON")
	}

}

func validateContentType(allowedTypes ...string) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		allowed := make(map[string]bool)
		for _, t := range allowedTypes {
			allowed[t] = true
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			contentType := r.Header.Get("Content-Type")
			if !allowed[contentType] {
				logrus.WithFields(logrus.Fields{
					"contentType": contentType,
					"remoteAddr":  r.RemoteAddr,
					"requestUri":  r.RequestURI,
					"protocol":    r.Proto,
				}).Error("Invalid content type received from client")
				http.Error(w, "invalid Content-Type, want `application/json`", http.StatusUnsupportedMediaType)
			} else {
				next.ServeHTTP(w, r)
			}
		})
	}
}

func errorResponse(err error, status int, ar *v1beta1.AdmissionReview, w http.ResponseWriter) {
	response := v1beta1.AdmissionReview{}
	response.Response = ToAdmissionResponse(err)
	if ar != nil && ar.Request != nil {
		response.Response.UID = ar.Request.UID
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		logrus.WithError(err).Error("Failed to marshal error response")
	} else {
		w.Write([]byte(fmt.Sprintf("[ERROR]: %s", err)))
	}
}

// TODO support more types
func getAnnotations(obj runtime.Object) map[string]string {
	var annotations map[string]string
	switch v := obj.(type) {
	case *corev1.Pod:
		annotations = v.Annotations
	case *appsv1.Deployment:
		annotations = v.Annotations
	case *appsv1.DaemonSet:
		annotations = v.Annotations
	default:
		annotations = map[string]string{}
	}
	return annotations
}

// AnnotationHasValue checks whether an API object has annotations and these annotations
// contain the specified key with the specified value
func AnnotationHasValue(obj runtime.Object, key, val string) bool {
	annotations := getAnnotations(obj)
	v, e := annotations[key]
	return e && v == val
}

// AnnotationValue retrieves the string representation of the value of an annotation specified
// by key. In case the annotation is not found a default value can be specified as the last parameter
func AnnotationValue(obj runtime.Object, key string, def ...string) string {
	annotations := getAnnotations(obj)
	if len(def) == 0 {
		return annotations[key]
	}
	if val, exists := annotations[key]; exists {
		return val
	} else {
		return def[0]
	}
}

// ToAdmissionResponse is a simple method to create a v1beta1.AdmissionResponse struct with an
// error message set
func ToAdmissionResponse(err error) *v1beta1.AdmissionResponse {
	return &v1beta1.AdmissionResponse{Result: &metav1.Status{Message: err.Error()}}
}

// CreatePatch creates a JSON patch from the given mutatedObj and its JSON serialized
// original structure.
func CreatePatch(mutatedObj runtime.Object, objRaw []byte) ([]byte, error) {
	mutatedRawBuf := &bytes.Buffer{}
	if err := Marshaler.Encode(mutatedObj, mutatedRawBuf); err != nil {
		return nil, err
	}
	patch, err := jsonpatch.CreatePatch(objRaw, mutatedRawBuf.Bytes())
	if err != nil {
		return nil, err
	}

	if len(patch) > 0 {
		// ignore creationTimestamp
		for _, path := range ignoredPatchPaths {
			for i, p := range patch {
				if p.Path == path {
					patch = append(patch[:i], patch[i+1:]...)
				}
			}
		}
		return json.Marshal(patch)
	}
	return nil, nil
}
