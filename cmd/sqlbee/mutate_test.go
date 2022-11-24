package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"k8s.io/api/admission/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/mattbaird/jsonpatch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testPod = `
apiVersion: v1
kind: Pod
metadata:
  name: wordpress
  labels:
    app: wordpress
  annotations:
    sqlbee.connctd.io.inject: "true"
spec:
  containers:
  - image: wordpress:4.8-apache
    name: wordpress
    env:
    - name: WORDPRESS_DB_HOST
      value: wordpress-mysql
    - name: WORDPRESS_DB_PASSWORD
      valueFrom:
        secretKeyRef:
          name: mysql-pass
          key: password
    ports:
    - containerPort: 80
      name: wordpress
    volumeMounts:
    - name: wordpress-persistent-storage
      mountPath: /var/www/html
  volumes:
  - name: wordpress-persistent-storage
    persistentVolumeClaim:
      claimName: wp-pv-claim
`

var podJson = `
{
   "apiVersion": "v1",
   "kind": "Pod",
   "metadata": {
      "name": "wordpress",
      "labels": {
         "app": "wordpress"
      },
      "annotations": {
         "sqlbee.connctd.io.inject": "true"
      }
   },
   "spec": {
      "containers": [
         {
            "image": "wordpress:4.8-apache",
            "name": "wordpress",
            "env": [
               {
                  "name": "WORDPRESS_DB_HOST",
                  "value": "wordpress-mysql"
               },
               {
                  "name": "WORDPRESS_DB_PASSWORD",
                  "valueFrom": {
                     "secretKeyRef": {
                        "name": "mysql-pass",
                        "key": "password"
                     }
                  }
               }
            ],
            "ports": [
               {
                  "containerPort": 80,
                  "name": "wordpress"
               }
            ],
            "volumeMounts": [
               {
                  "name": "wordpress-persistent-storage",
                  "mountPath": "/var/www/html"
               }
            ]
         }
      ],
      "volumes": [
         {
            "name": "wordpress-persistent-storage",
            "persistentVolumeClaim": {
               "claimName": "wp-pv-claim"
            }
         }
      ]
   }
}
`

var expectedPodPatches = `[{"op":"add","path":"/spec/volumes/1","value":{"emptyDir":{},"name":"cloudsql"}},{"op":"add","path":"/spec/volumes/2","value":{"name":"sql-service-token-account","secret":{"secretName":"cloud-sql-credentials"}}},{"op":"remove","path":"/spec/containers/0"},{"op":"add","path":"/spec/containers/0","value":{"env":[{"name":"WORDPRESS_DB_HOST","value":"wordpress-mysql"},{"name":"WORDPRESS_DB_PASSWORD","valueFrom":{"secretKeyRef":{"key":"password","name":"mysql-pass"}}}],"image":"wordpress:4.8-apache","name":"wordpress","ports":[{"containerPort":80,"name":"wordpress"}],"resources":{},"volumeMounts":[{"mountPath":"/var/www/html","name":"wordpress-persistent-storage"}]}},{"op":"add","path":"/spec/containers/1","value":{"command":["/cloud_sql_proxy","-dir=/cloudsql","-credential_file=/credentials/credentials.json","-instances=my-gcp-project-42:europe-west1:sql-master=tcp:127.0.0.1:3306"],"image":"gcr.io/cloudsql-docker/gce-proxy:1.33.1","name":"cloud-sql-proxy","resources":{"requests":{"cpu":"10m","memory":"16Mi"}},"volumeMounts":[{"mountPath":"/cloudsql","name":"cloudsql"},{"mountPath":"/credentials","name":"sql-service-token-account"}]}}]`

func TestMutation(t *testing.T) {
	podRequest := &v1beta1.AdmissionReview{
		Request: &v1beta1.AdmissionRequest{
			Resource: podResource,
			Object: runtime.RawExtension{
				Raw: []byte(podJson),
			},
		},
	}

	for _, data := range []struct {
		review          *v1beta1.AdmissionReview
		allowed         bool
		expectedPatches string
	}{
		{
			review:          podRequest,
			allowed:         true,
			expectedPatches: expectedPodPatches,
		},
	} {
		mutateOpts := Options{}
		mutateOpts.DefaultInstance = "my-gcp-project-42:europe-west1:sql-master"
		mutateOpts.DefaultSecretName = "cloud-sql-credentials"
		mutateOpts.RequireAnnotation = true
		mut := Mutate(mutateOpts)

		ar := mut(data.review)
		require.NotNil(t, ar)

		assert.Equal(t, data.allowed, ar.Allowed)
		if data.allowed {
			assert.Equal(t, v1beta1.PatchTypeJSONPatch, *ar.PatchType)
			assert.NotEmpty(t, ar.Patch)

			equal, err := AreEqualPatches(data.expectedPatches, string(ar.Patch))
			require.NoError(t, err)
			assert.True(t, equal, "Patches differ from expected patches.\nExpected:\n%s\nActual:\n%s\n", data.expectedPatches, string(ar.Patch))
		} else {
			assert.Empty(t, ar.Patch)
		}
	}
}

func AreEqualPatches(s1, s2 string) (bool, error) {
	var o1 []jsonpatch.JsonPatchOperation
	var o2 []jsonpatch.JsonPatchOperation

	var err error
	err = json.Unmarshal([]byte(s1), &o1)
	if err != nil {
		return false, fmt.Errorf("Error mashalling string 1 :: %s", err.Error())
	}
	err = json.Unmarshal([]byte(s2), &o2)
	if err != nil {
		return false, fmt.Errorf("Error mashalling string 2 :: %s", err.Error())
	}
	sort.Stable(jsonpatch.ByPath(o1))
	sort.Stable(jsonpatch.ByPath(o2))

	return reflect.DeepEqual(o1, o2), nil
}
