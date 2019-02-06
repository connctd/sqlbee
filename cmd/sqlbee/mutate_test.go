package main

import (
	"fmt"
	"testing"

	"k8s.io/api/admission/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testDeployment = `
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

var deploymentJson = `
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

var expectedPatches = `[{"op":"add","path":"/spec/volumes/1","value":{"emptyDir":{},"name":"cloudsql"}},{"op":"add","path":"/spec/volumes/2","value":{"name":"sql-service-token-account","secret":{"secretName":"cloud-sql-proxy-credentials"}}},{"op":"add","path":"/spec/volumes/3","value":{"configMap":{"name":"ca-certificates"},"name":"sql-ca-certificates"}},{"op":"remove","path":"/spec/containers/0"},{"op":"add","path":"/spec/containers/0","value":{"env":[{"name":"WORDPRESS_DB_HOST","value":"wordpress-mysql"},{"name":"WORDPRESS_DB_PASSWORD","valueFrom":{"secretKeyRef":{"key":"password","name":"mysql-pass"}}}],"image":"wordpress:4.8-apache","name":"wordpress","ports":[{"containerPort":80,"name":"wordpress"}],"resources":{},"volumeMounts":[{"mountPath":"/var/www/html","name":"wordpress-persistent-storage"}]}},{"op":"add","path":"/spec/containers/1","value":{"command":["/cloud_sql_proxy","-dir=/cloudsql","credential_file=/credentials/credentials.json","instances=my-gcp-project-42:europe-west1:sql-master=tcp:127.0.0.1:3306"],"image":"gcr.io/cloudsql-docker/gce-proxy:1.08","name":"cloud-sql-proxy","resources":{},"volumeMounts":[{"mountPath":"/cloudsql","name":"cloudsql"},{"mountPath":"/credentials","name":"service-token-account"},{"mountPath":"/etc/ssl/certs","name":"ca-certificates"}]}},{"op":"add","path":"/status","value":{}},{"op":"add","path":"/metadata/creationTimestamp","value":null}]`

func TestSqlProxyInjectionInPod(t *testing.T) {
	request := &v1beta1.AdmissionReview{
		Request: &v1beta1.AdmissionRequest{
			Resource: podResource,
			Object: runtime.RawExtension{
				Raw: []byte(deploymentJson),
			},
		},
	}
	mutateOpts := Options{}
	mutateOpts.DefaultInstance = "my-gcp-project-42:europe-west1:sql-master"
	mut := Mutate(mutateOpts)

	ar := mut(request)
	require.NotNil(t, ar)

	assert.True(t, ar.Allowed)
	assert.Equal(t, v1beta1.PatchTypeJSONPatch, *ar.PatchType)
	assert.NotEmpty(t, ar.Patch)
	fmt.Printf("Patches:\n%s\n", string(ar.Patch))
	// TODO find safer way to ensure necessary patch operations are present
	//assert.Equal(t, expectedPatches, string(ar.Patch))
}
