package main

import (
	"fmt"

	"github.com/sirupsen/logrus"
	"k8s.io/api/admission/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/connctd/sqlbee/pkg/sting"
)

var (
	podResource              = metav1.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	deploymentResource       = metav1.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	legacyDeploymentResource = metav1.GroupVersionResource{Group: "extensions", Version: "v1beta1", Resource: "deployments"}

	// Annotations which are checked and used to influence the injection
	annotationBase     = "sqlbee.connctd.io."
	annotationInject   = annotationBase + "inject"
	annotationImage    = annotationBase + "image"
	annotationInstance = annotationBase + "instance"
	annotationSecret   = annotationBase + "secret"
	annotationCaMap    = annotationBase + "caMap"

	// default image to be used if none is specified
	imageName    = "gcr.io/cloudsql-docker/gce-proxy"
	imageTag     = "1.13"
	defaultImage = imageName + ":" + imageTag

	// Command of the sql proxy container. Is extended througout the injection process with additional
	// parameters depending on the configuration and annotations
	sqlProxyCmd = []string{
		"/cloud_sql_proxy",
		"-dir=/cloudsql",
	}

	// Predefined definition to mount GCP credentials
	credentialMount = corev1.VolumeMount{
		MountPath: "/credentials",
		Name:      "sql-service-token-account",
	}

	// Predefined definition to mount different root certificates
	caCertMount = corev1.VolumeMount{
		MountPath: "/etc/ssl/certs",
		Name:      "ca-certificates",
	}

	// barebones container specification for the cloud sql proxy sidecar. Is extended throughout the
	// injection process
	sqlProxyContainer = corev1.Container{
		Image:   defaultImage,
		Command: sqlProxyCmd,
		VolumeMounts: []corev1.VolumeMount{
			corev1.VolumeMount{
				MountPath: "/cloudsql",
				Name:      "cloudsql",
			},
		},
		Name: "cloud-sql-proxy",
	}

	// definition of a volume mount for GCP credentials. Will be modified during the injection process
	credentialsVolume = corev1.Volume{
		Name: "sql-service-token-account",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: "cloud-sql-proxy-credentials",
			},
		},
	}

	// definition of a volume to mount custom ca certificates
	caCertVolume = corev1.Volume{
		Name: "sql-ca-certificates",
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: "ca-certificates",
				},
			},
		},
	}

	// the cloud sql proxy will always have this single volume to write temporary data to it
	sqlProxyVolumes = []corev1.Volume{
		corev1.Volume{
			Name: "cloudsql",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}
)

var (
	// injection requests for these namespaces are ignored
	ignoredNamespaces = []string{
		metav1.NamespaceSystem,
		metav1.NamespacePublic,
	}
)

// Options describe the possible injection options
type Options struct {
	// The cloud sql instance(s) the cloud sql proxy will forward connections to
	DefaultInstance string
	// The secret containing the cloud sql credentials if not specified by annotations
	DefaultSecretName string
	// The config map containing the root certificates, if necessary
	DefaultCertVolume string
	// Whether injection should only happen if the inject annotation is present and set to true
	RequireAnnotation bool
}

// mutates a corev1.PodSpec to contain a cloud sql proxy sidecar and the necessary volume mounts and volumes
func mutatePodSpec(volumes []corev1.Volume, proxyContainer *corev1.Container, podSpec *corev1.PodSpec) corev1.PodSpec {

	for i, container := range podSpec.Containers {
		if container.Image == proxyContainer.Image || container.Name == proxyContainer.Name {
			// If a cloud sql proxy already exists, remove it
			podSpec.Containers = append(podSpec.Containers[:i], podSpec.Containers[i+1:]...)
			break
		}
	}
	podSpec.Containers = append(podSpec.Containers, *proxyContainer)

	for i, volume := range podSpec.Volumes {
		// Remove possibly existing volumes cloud sql proxy relies on and add them later again
		if volume.Name == "cloudsql" || volume.Name == "sql-service-token-account" || volume.Name == "sql-ca-certificates" {
			podSpec.Volumes = append(podSpec.Volumes[:i], podSpec.Volumes[i+1:]...)
		}
	}
	podSpec.Volumes = append(podSpec.Volumes, volumes...)
	return *podSpec
}

// configures the sidecar container spec and the required volumes for the podSpec based on the provided options
func configureContainerAndVolumes(obj runtime.Object, sqlProxyContainer *corev1.Container, sqlProxyVolumes *[]corev1.Volume, opts Options) {
	image := sting.AnnotationValue(obj, annotationImage, defaultImage)
	sqlProxyContainer.Image = image
	cmd := []string{}
	cmd = append(cmd, sqlProxyCmd...)

	instance := sting.AnnotationValue(obj, annotationInstance, opts.DefaultInstance)

	secretName := sting.AnnotationValue(obj, annotationSecret, opts.DefaultSecretName)
	if secretName != "" {
		sqlProxyContainer.VolumeMounts = append(sqlProxyContainer.VolumeMounts, credentialMount)
		credVolumes := credentialsVolume.DeepCopy()
		credVolumes.VolumeSource.Secret.SecretName = secretName
		*sqlProxyVolumes = append(*sqlProxyVolumes, *credVolumes)
		cmd = append(cmd, "-credential_file=/credentials/credentials.json")
	}

	caConfigName := sting.AnnotationValue(obj, annotationCaMap, opts.DefaultCertVolume)
	if caConfigName != "" {
		caVolume := caCertVolume.DeepCopy()
		caVolume.VolumeSource.ConfigMap.Name = caConfigName
		sqlProxyContainer.VolumeMounts = append(sqlProxyContainer.VolumeMounts, caCertMount)
		*sqlProxyVolumes = append(*sqlProxyVolumes, *caVolume)
	}

	cmd = append(cmd, fmt.Sprintf("-instances=%s=tcp:127.0.0.1:3306", instance))

	sqlProxyContainer.Command = cmd
}

// Mutate returns a sting.MutateFunc parametrized with the specified Options
func Mutate(opts Options) sting.MutateFunc {

	return func(ar *v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {

		reviewResponse := &v1beta1.AdmissionResponse{}

		// Ignore certain namespaces
		for _, namespace := range ignoredNamespaces {
			if ar.Request.Namespace == namespace {
				logrus.WithFields(logrus.Fields{
					"requestUID": ar.Request.UID,
					"resource":   ar.Request.Resource.String(),
					"name":       ar.Request.Name,
					"namespace":  ar.Request.Namespace,
				}).Info("Mutation ignored because of the namespace")
				reviewResponse.Allowed = true
				return reviewResponse
			}
		}

		// Create a deep copy of the sidecar container and the volumes so multiple
		// requests don't try to manipulate the same objects in memory
		proxyContainer := sqlProxyContainer.DeepCopy()
		volumes := make([]corev1.Volume, 0, 5)
		volumes = append(volumes, sqlProxyVolumes...)

		raw := ar.Request.Object.Raw
		var obj runtime.Object
		var podSpec *corev1.PodSpec

		if ar.Request.Resource == podResource {
			logrus.Info("Mutating pod resource")

			pod := &corev1.Pod{}
			// Deserialize a pod object
			if _, _, err := sting.Deserializer.Decode(raw, nil, pod); err != nil {
				logrus.WithError(err).WithFields(logrus.Fields{
					"requestUID": ar.Request.UID,
				}).Error("Failed to deserialize pod object")
				return sting.ToAdmissionResponse(err)
			}

			obj = pod
			podSpec = &pod.Spec
			// Check if we are dealing with any deployment
		} else if ar.Request.Resource.Resource == "deployments" {
			logrus.Info("Mutating deployment")
			deployment := &appsv1.Deployment{}
			if _, _, err := sting.Deserializer.Decode(raw, nil, deployment); err != nil {
				logrus.WithError(err).WithFields(logrus.Fields{
					"requestUID": ar.Request.UID,
				}).Error("Faiedl to deserialize deployment object")
				return sting.ToAdmissionResponse(err)
			}
			obj = deployment
			podSpec = &deployment.Spec.Template.Spec
		} else {
			// In case we misconfigure the admission webhook return an error
			logrus.WithFields(logrus.Fields{
				"requestUID": ar.Request.UID,
				"resource":   ar.Request.Resource.String(),
			}).Error("Received unknown resource")
			return sting.ToAdmissionResponse(sting.WrongResourceError)
		}

		// Check whether we should do the mutation. If the inject annotation is true
		// we always inject. If it is false we never muate. If it is missing it depends
		// whether opts.RequireAnnotation is true or not.
		if opts.RequireAnnotation && !sting.AnnotationHasValue(obj, annotationInject, "true") {
			logrus.WithFields(logrus.Fields{
				"requestUID": ar.Request.UID,
				"resource":   ar.Request.Resource.String(),
			}).Info("Resource does not need mutation, allowed")
			reviewResponse.Allowed = true
			return reviewResponse
		} else if !opts.RequireAnnotation && sting.AnnotationHasValue(obj, annotationInject, "false") {
			logrus.WithFields(logrus.Fields{
				"requestUID": ar.Request.UID,
				"resource":   ar.Request.Resource.String(),
			}).Info("Resource does not need mutation, allowed")
			reviewResponse.Allowed = true
			return reviewResponse
		}

		//Check if we have a valid cloud sql instance
		if sting.AnnotationValue(obj, annotationInstance, opts.DefaultInstance) == "" {
			logrus.WithFields(logrus.Fields{
				"requestUID": ar.Request.UID,
				"resource":   ar.Request.Resource.String(),
				"name":       ar.Request.Name,
				"namespace":  ar.Request.Namespace,
			}).Error("Can't determine Cloud SQL instance, SQLBee is not correctly configured")
			err := fmt.Errorf("Instance is not specified via defaults or via annotation %s", annotationInstance)
			return sting.ToAdmissionResponse(err)
		}

		// After here we should have all necessary information and be sure that we want to do
		// the mutation
		reviewResponse.Allowed = true

		// Configure our copies of the container spec and the volumes based on the annotations
		// and configuration
		configureContainerAndVolumes(obj, proxyContainer, &volumes, opts)
		// mutate the pod with our sidecar and volumes
		mutatePodSpec(volumes, proxyContainer, podSpec)
		// create the actual patch
		patchBytes, err := sting.CreatePatch(obj, raw)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"requestUID": ar.Request.UID,
				"resource":   ar.Request.Resource.String(),
				"name":       ar.Request.Name,
				"namespace":  ar.Request.Namespace,
			}).Error("Failed to create JSON patch")
			return sting.ToAdmissionResponse(err)
		}

		// Only set the patch type if we have actually something to patch
		if len(patchBytes) > 0 {
			pt := v1beta1.PatchTypeJSONPatch
			reviewResponse.PatchType = &pt
			reviewResponse.Patch = patchBytes
			logrus.WithFields(logrus.Fields{
				"patch": string(patchBytes),
			}).Debug("Created patches")
		}
		logrus.WithFields(logrus.Fields{
			"requestUID": ar.Request.UID,
			"resource":   ar.Request.Resource.String(),
			"name":       ar.Request.Name,
			"namespace":  ar.Request.Namespace,
		}).Info("Resource mutated, cloud-sql-proxy sidecar injected")
		return reviewResponse
	}
}
