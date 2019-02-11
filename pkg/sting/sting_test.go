package sting

import (
	"bytes"
	"errors"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"
)

func TestAnnotationHasValue(t *testing.T) {

	for _, data := range []struct {
		annotations map[string]string
		key         string
		val         string
		expected    bool
	}{
		{
			map[string]string{"foo": "bar"},
			"foo",
			"bar",
			true,
		},
		{
			map[string]string{},
			"foo",
			"bar",
			false,
		},
		{
			nil,
			"foo",
			"bar",
			false,
		},
		{
			map[string]string{"foo": "baz"},
			"foo",
			"bar",
			false,
		},
	} {
		obj := &corev1.Pod{}
		obj.Annotations = data.annotations

		assert.Equal(t, data.expected, AnnotationHasValue(obj, data.key, data.val))
	}
}

func TestAnnotationValue(t *testing.T) {
	for _, data := range []struct {
		annotations map[string]string
		key         string
		expected    string
	}{
		{
			map[string]string{"foo": "bar"},
			"foo",
			"bar",
		},
		{
			map[string]string{},
			"foo",
			"",
		},
		{
			nil,
			"foo",
			"",
		},
		{
			map[string]string{"fop": "bar"},
			"foo",
			"",
		},
	} {
		obj := &corev1.Pod{}
		obj.Annotations = data.annotations

		assert.Equal(t, data.expected, AnnotationValue(obj, data.key))
	}
}

func TestReadRequest(t *testing.T) {

	podReview := `
{"kind":"AdmissionReview","apiVersion":"admission.k8s.io/v1beta1","request":{"uid":"27f5fa18-2dfe-11e9-9012-025000000001","kind":{"group":"","version":"v1","kind":"Pod"},"resource":{"group":"","version":"v1","resource":"pods"},"namespace":"default","operation":"CREATE","userInfo":{"username":"system:serviceaccount:kube-system:replicaset-controller","uid":"e5fb7268-2dfa-11e9-9012-025000000001","groups":["system:serviceaccounts","system:serviceaccounts:kube-system","system:authenticated"]},"object":{"metadata":{"generateName":"echoheaders-deployment-6f8fb55c79-","creationTimestamp":null,"labels":{"app":"echoheaders","channel":"stable","pod-template-hash":"2949611735","svc":"echoheaders"},"annotations":{"sqlbee.connctd.io.image":"gcr.io/cloudsql-docker/gce-proxy:1.08","sqlbee.connctd.io.inject":"true"},"ownerReferences":[{"apiVersion":"extensions/v1beta1","kind":"ReplicaSet","name":"echoheaders-deployment-6f8fb55c79","uid":"27f37176-2dfe-11e9-9012-025000000001","controller":true,"blockOwnerDeletion":true}]},"spec":{"volumes":[{"name":"default-token-fw9dz","secret":{"secretName":"default-token-fw9dz"}}],"containers":[{"name":"echoheaders","image":"k8s.gcr.io/echoserver:1.4","ports":[{"containerPort":8080,"protocol":"TCP"}],"resources":{},"volumeMounts":[{"name":"default-token-fw9dz","readOnly":true,"mountPath":"/var/run/secrets/kubernetes.io/serviceaccount"}],"terminationMessagePath":"/dev/termination-log","terminationMessagePolicy":"File","imagePullPolicy":"IfNotPresent"}],"restartPolicy":"Always","terminationGracePeriodSeconds":30,"dnsPolicy":"ClusterFirst","serviceAccountName":"default","serviceAccount":"default","securityContext":{},"schedulerName":"default-scheduler","tolerations":[{"key":"node.kubernetes.io/not-ready","operator":"Exists","effect":"NoExecute","tolerationSeconds":300},{"key":"node.kubernetes.io/unreachable","operator":"Exists","effect":"NoExecute","tolerationSeconds":300}]},"status":{}},"oldObject":null}}
  `
	podReviewBuf := &bytes.Buffer{}
	podReviewBuf.WriteString(podReview)

	for _, data := range []struct {
		request        *http.Request
		expectedError  error
		expectedStatus int
	}{
		{
			request: &http.Request{
				Body: ioutil.NopCloser(podReviewBuf),
			},
			expectedError:  nil,
			expectedStatus: 200,
		},
		{
			request: &http.Request{
				Body: ioutil.NopCloser(&bytes.Buffer{}),
			},
			expectedError:  errors.New("Request body is empty"),
			expectedStatus: http.StatusBadRequest,
		},
	} {
		w := httptest.NewRecorder()
		ar, err := readRequest(w, data.request)
		assert.Equal(t, data.expectedError, err)
		w.Flush()
		assert.Equal(t, data.expectedStatus, w.Code)
		if data.expectedError == nil {
			assert.NotNil(t, ar)
		} else {
			assert.Nil(t, ar)
		}
	}
}
