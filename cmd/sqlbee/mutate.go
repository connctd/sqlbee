package main

import (
	"bytes"
	stdjson "encoding/json"
	"fmt"

	"github.com/mattbaird/jsonpatch"
	"github.com/sirupsen/logrus"
	"k8s.io/api/admission/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"

	"github.com/connctd/sqlbee/sting"
)

var (
	serializer = json.NewSerializer(json.DefaultMetaFactory, sting.RuntimeScheme, sting.RuntimeScheme, false)

	podResource              = metav1.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	deploymentResource       = metav1.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	legacyDeploymentResource = metav1.GroupVersionResource{Group: "extensions", Version: "v1beta1", Resource: "deployments"}

	annotationBase     = "sqlbee.connctd.io."
	annotationInject   = annotationBase + "inject"
	annotationImage    = annotationBase + "image"
	annotationInstance = annotationBase + "instance"
	annotationSecret   = annotationBase + "secret"
	annotationCaMap    = annotationBase + "caMap"

	imageName    = "gcr.io/cloudsql-docker/gce-proxy"
	imageTag     = "1.13"
	defaultImage = imageName + ":" + imageTag

	sqlProxyCmd = []string{
		"/cloud_sql_proxy",
		"-dir=/cloudsql",
	}

	credentialMount = corev1.VolumeMount{
		MountPath: "/credentials",
		Name:      "service-token-account",
	}

	caCertMount = corev1.VolumeMount{
		MountPath: "/etc/ssl/certs",
		Name:      "ca-certificates",
	}

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

	credentialsVolume = corev1.Volume{
		Name: "sql-service-token-account",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: "cloud-sql-proxy-credentials",
			},
		},
	}

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

	sqlProxyVolumes = []corev1.Volume{
		corev1.Volume{
			Name: "cloudsql",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}
)

type Options struct {
	DefaultInstance   string
	DefaultSecretName string
	DefaultCertVolume string
}

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

func configureContainerAndVolumes(obj runtime.Object, sqlProxyContainer *corev1.Container, sqlProxyVolumes []corev1.Volume, opts Options) {
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
		sqlProxyVolumes = append(sqlProxyVolumes, *credVolumes)
		cmd = append(cmd, "credential_file=/credentials/credentials.json")
	}

	caConfigName := sting.AnnotationValue(obj, annotationCaMap, opts.DefaultCertVolume)
	if caConfigName != "" {
		caVolume := caCertVolume.DeepCopy()
		caVolume.VolumeSource.ConfigMap.Name = caConfigName
		sqlProxyContainer.VolumeMounts = append(sqlProxyContainer.VolumeMounts, caCertMount)
		sqlProxyVolumes = append(sqlProxyVolumes, *caVolume)
	}

	cmd = append(cmd, fmt.Sprintf("instances=%s=tcp:127.0.0.1:3306", instance))

	sqlProxyContainer.Command = cmd
}

func createPatch(mutatedObj runtime.Object, objRaw []byte) ([]byte, error) {
	mutatedRawBuf := &bytes.Buffer{}
	if err := serializer.Encode(mutatedObj, mutatedRawBuf); err != nil {
		return nil, err
	}
	patch, err := jsonpatch.CreatePatch(objRaw, mutatedRawBuf.Bytes())
	if err != nil {
		return nil, err
	}

	if len(patch) > 0 {
		// ignore creationTimestamp
		for i, p := range patch {
			if p.Path == "/metadata/creationTimestamp" {
				patch = append(patch[:i], patch[i+1:]...)
			}
		}
		return stdjson.Marshal(patch)
	}
	return nil, nil
}

func Mutate(opts Options) sting.MutateFunc {

	return func(ar *v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {

		proxyContainer := sqlProxyContainer.DeepCopy()
		volumes := []corev1.Volume{}
		volumes = append(volumes, sqlProxyVolumes...)

		reviewResponse := &v1beta1.AdmissionResponse{}

		raw := ar.Request.Object.Raw
		var obj runtime.Object
		var podSpec *corev1.PodSpec

		if ar.Request.Resource == podResource {
			logrus.Info("Mutating pod resource")

			pod := &corev1.Pod{}
			if _, _, err := sting.Deserializer.Decode(raw, nil, pod); err != nil {
				return sting.ToAdmissionResponse(err)
			}

			obj = pod
			podSpec = &pod.Spec
		} else if ar.Request.Resource == deploymentResource || ar.Request.Resource == legacyDeploymentResource {
			deployment := &appsv1.Deployment{}
			if _, _, err := sting.Deserializer.Decode(raw, nil, deployment); err != nil {
				return sting.ToAdmissionResponse(err)
			}
			obj = deployment
			podSpec = &deployment.Spec.Template.Spec
		} else {
			return sting.ToAdmissionResponse(sting.WrongResourceError)
		}

		if !sting.AnnotationHasValue(obj, annotationInject, "true") {
			reviewResponse.Allowed = true
			return reviewResponse
		}

		//Check if we have a valid cloud sql instance
		if sting.AnnotationValue(obj, annotationInstance, opts.DefaultInstance) == "" {
			err := fmt.Errorf("Instance is not specified via defaults or via annotation %s", annotationInstance)
			return sting.ToAdmissionResponse(err)
		}

		reviewResponse.Allowed = true

		configureContainerAndVolumes(obj, proxyContainer, volumes, opts)
		mutatePodSpec(volumes, proxyContainer, podSpec)
		patchBytes, err := createPatch(obj, raw)
		if err != nil {
			return sting.ToAdmissionResponse(err)
		}

		if len(patchBytes) > 0 {
			pt := v1beta1.PatchTypeJSONPatch
			reviewResponse.PatchType = &pt
			reviewResponse.Patch = patchBytes
		}

		return reviewResponse
	}
}
