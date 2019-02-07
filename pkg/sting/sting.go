package sting

import (
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
	"github.com/sirupsen/logrus"

	"k8s.io/api/admission/v1beta1"
	admissionregistrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/kubernetes/pkg/apis/core/v1"
)

var (
	WrongResourceError = errors.New("Wrong resource type")
)

var (
	RuntimeScheme = runtime.NewScheme()
	Codecs        = serializer.NewCodecFactory(RuntimeScheme)
	Deserializer  = Codecs.UniversalDeserializer()

	// (https://github.com/kubernetes/kubernetes/issues/57982)
	Defaulter = runtime.ObjectDefaulter(RuntimeScheme)
)

var (
	ignoredNamespaces = []string{
		metav1.NamespaceSystem,
		metav1.NamespacePublic,
	}
)

type patchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

func init() {
	_ = corev1.AddToScheme(RuntimeScheme)
	_ = appsv1.AddToScheme(RuntimeScheme)
	_ = admissionregistrationv1beta1.AddToScheme(RuntimeScheme)
	// defaulting with webhooks:
	// https://github.com/kubernetes/kubernetes/issues/57982
	_ = v1.AddToScheme(RuntimeScheme)
}

type MutateFunc func(ar *v1beta1.AdmissionReview) *v1beta1.AdmissionResponse
type NeedsMutationFunc func(ar *v1beta1.AdmissionReview) bool
type IsAdmittedFunc func(ar *v1beta1.AdmissionReview) (*v1beta1.AdmissionResponse, error)

type InjectServer struct {
	server   *http.Server
	cert     *tls.Certificate
	certLock *sync.Mutex

	mutate      MutateFunc
	needsMutate NeedsMutationFunc
	isAdmitted  IsAdmittedFunc
}

type Options struct {
	ListenAddr  string
	Mutate      MutateFunc
	NeedsMutate NeedsMutationFunc
	IsAdmitted  IsAdmittedFunc

	ReadTimeout       time.Duration
	IdleTimeout       time.Duration
	ReadHeaderTimeout time.Duration
	WriteTimeout      time.Duration

	CertFile string
	KeyFile  string
	CaFile   string
}

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

func NewOptions() *Options {
	return &Options{
		ListenAddr:        ":443",
		ReadTimeout:       time.Second * 10,
		IdleTimeout:       time.Second * 10,
		ReadHeaderTimeout: time.Second * 2,
		WriteTimeout:      time.Second * 10,
	}
}

func New(opts *Options) (*InjectServer, error) {
	i := &InjectServer{
		mutate:      opts.Mutate,
		needsMutate: opts.NeedsMutate,
		isAdmitted:  opts.IsAdmitted,
		certLock:    &sync.Mutex{},
	}

	pair, err := tls.LoadX509KeyPair(opts.CertFile, opts.KeyFile)
	if err != nil {
		return nil, err
	}
	i.cert = &pair

	r := mux.NewRouter()
	r.Use(validateContentType("application/json"))

	if opts.Mutate != nil {
		r.Path("/api/v1beta/mutate").Methods(http.MethodPost).HandlerFunc(i.handleMutate)
	}

	if opts.IsAdmitted != nil {
		r.Path("/api/v1beta/admit").Methods(http.MethodPost).HandlerFunc(i.handleAdmission)
	}

	server := &http.Server{
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

	certWatcher, err := fsnotify.NewWatcher()
	if err := certWatcher.Watch(opts.CertFile); err != nil {
		return nil, err
	}

	go func(watcher *fsnotify.Watcher, opts *Options) {
		for {
			select {
			case ev := <-watcher.Event:
				if ev.IsModify() || ev.IsCreate() {
					pair, err := tls.LoadX509KeyPair(opts.CertFile, opts.KeyFile)
					if err == nil {
						i.certLock.Lock()
						i.cert = &pair
						i.certLock.Unlock()
					} else {
						// TODO Log this error
					}
				}
			}
		}
	}(certWatcher, opts)

	i.server = server

	go func() {
		// TODO handle and log possible errors
		if err := i.server.ListenAndServeTLS("", ""); err != nil {
			logrus.WithError(err).Error("Failed to listen as TLS server")
		}
	}()

	return i, nil
}

func (i *InjectServer) Close() error {
	shutdownCtx, _ := context.WithTimeout(context.Background(), 15*time.Second)
	i.server.Shutdown(shutdownCtx)
	return nil
}

func (i *InjectServer) getCert(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	i.certLock.Lock()
	defer i.certLock.Unlock()
	return i.cert, nil
}

func readRequest(w http.ResponseWriter, r *http.Request) (*v1beta1.AdmissionReview, error) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		err := fmt.Errorf("Failed to read request body")
		errorResponse(err, http.StatusBadRequest, nil, w)
		return nil, fmt.Errorf("Failed to read request body")
	}

	if len(body) == 0 {
		err := fmt.Errorf("Request body is empty")
		errorResponse(err, http.StatusBadRequest, nil, w)
		return nil, err
	}

	ar := v1beta1.AdmissionReview{}
	if err := json.Unmarshal(body, &ar); err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"remoteAddr": r.RemoteAddr,
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
		return
	}

	ar, err := readRequest(w, r)
	if err != nil {
		return
	}

	var admissionResponse *v1beta1.AdmissionResponse
	response := v1beta1.AdmissionReview{}

	if i.needsMutate != nil && !i.needsMutate(ar) {
		admissionResponse = &v1beta1.AdmissionResponse{}
		admissionResponse.Allowed = true
		admissionResponse.Result = &metav1.Status{Message: "This resource does not need mutation"}
		response.Response = admissionResponse
	} else {
		admissionResponse = i.mutate(ar)
		if admissionResponse == nil {
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
			"requestUID": ar.Request.UID,
		}).Error("Failed to serialize admission response to JSON")
	}
}

func (i *InjectServer) handleAdmission(w http.ResponseWriter, r *http.Request) {
	if i.isAdmitted == nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}

	ar, err := readRequest(w, r)
	if err != nil {
		return
	}

	admissionResponse, err := i.isAdmitted(ar)
	if err != nil {
		errorResponse(err, http.StatusNotAcceptable, ar, w)
		return
	}
	response := v1beta1.AdmissionReview{}
	response.Response = admissionResponse
	response.Response.UID = ar.Request.UID

	if err := json.NewEncoder(w).Encode(response); err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"requestUID": ar.Request.UID,
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

func AnnotationHasValue(obj runtime.Object, key, val string) bool {
	annotations := getAnnotations(obj)
	v, e := annotations[key]
	return e && v == val
}

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

func ToAdmissionResponse(err error) *v1beta1.AdmissionResponse {
	return &v1beta1.AdmissionResponse{Result: &metav1.Status{Message: err.Error()}}
}