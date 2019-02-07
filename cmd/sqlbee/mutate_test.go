package main

import (
	"testing"

	"k8s.io/api/admission/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"

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

var testDeployment = `
apiVersion: apps/v1 # for versions before 1.9.0 use apps/v1beta2
kind: Deployment
metadata:
  name: wordpress
  labels:
    app: wordpress
  annotations:
    sqlbee.connctd.io.inject: "true"
spec:
  selector:
    matchLabels:
      app: wordpress
      tier: frontend
  strategy:
    type: Recreate
  template:
    metadata:
      labels:
        app: wordpress
        tier: frontend
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

var deploymentJson = `
{
   "apiVersion": "apps/v1",
   "kind": "Deployment",
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
      "selector": {
         "matchLabels": {
            "app": "wordpress",
            "tier": "frontend"
         }
      },
      "strategy": {
         "type": "Recreate"
      },
      "template": {
         "metadata": {
            "labels": {
               "app": "wordpress",
               "tier": "frontend"
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
   }
}
`

var expectedPodPatches = `[{"op":"add","path":"/spec/volumes/1","value":{"emptyDir":{},"name":"cloudsql"}},{"op":"remove","path":"/spec/containers/0"},{"op":"add","path":"/spec/containers/0","value":{"env":[{"name":"WORDPRESS_DB_HOST","value":"wordpress-mysql"},{"name":"WORDPRESS_DB_PASSWORD","valueFrom":{"secretKeyRef":{"key":"password","name":"mysql-pass"}}}],"image":"wordpress:4.8-apache","name":"wordpress","ports":[{"containerPort":80,"name":"wordpress"}],"resources":{},"volumeMounts":[{"mountPath":"/var/www/html","name":"wordpress-persistent-storage"}]}},{"op":"add","path":"/spec/containers/1","value":{"command":["/cloud_sql_proxy","-dir=/cloudsql","instances=my-gcp-project-42:europe-west1:sql-master=tcp:127.0.0.1:3306"],"image":"gcr.io/cloudsql-docker/gce-proxy:1.13","name":"cloud-sql-proxy","resources":{},"volumeMounts":[{"mountPath":"/cloudsql","name":"cloudsql"}]}}]`

var expectedDeploymentPatches = `[{"op":"add","path":"/spec/template/spec/volumes/1","value":{"emptyDir":{},"name":"cloudsql"}},{"op":"remove","path":"/spec/template/spec/containers/0"},{"op":"add","path":"/spec/template/spec/containers/0","value":{"env":[{"name":"WORDPRESS_DB_HOST","value":"wordpress-mysql"},{"name":"WORDPRESS_DB_PASSWORD","valueFrom":{"secretKeyRef":{"key":"password","name":"mysql-pass"}}}],"image":"wordpress:4.8-apache","name":"wordpress","ports":[{"containerPort":80,"name":"wordpress"}],"resources":{},"volumeMounts":[{"mountPath":"/var/www/html","name":"wordpress-persistent-storage"}]}},{"op":"add","path":"/spec/template/spec/containers/1","value":{"command":["/cloud_sql_proxy","-dir=/cloudsql","instances=my-gcp-project-42:europe-west1:sql-master=tcp:127.0.0.1:3306"],"image":"gcr.io/cloudsql-docker/gce-proxy:1.13","name":"cloud-sql-proxy","resources":{},"volumeMounts":[{"mountPath":"/cloudsql","name":"cloudsql"}]}}]`

func TestMutation(t *testing.T) {
	podRequest := &v1beta1.AdmissionReview{
		Request: &v1beta1.AdmissionRequest{
			Resource: podResource,
			Object: runtime.RawExtension{
				Raw: []byte(podJson),
			},
		},
	}

	deploymentRequest := &v1beta1.AdmissionReview{
		Request: &v1beta1.AdmissionRequest{
			Resource: deploymentResource,
			Object: runtime.RawExtension{
				Raw: []byte(deploymentJson),
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
		{
			review:          deploymentRequest,
			allowed:         true,
			expectedPatches: expectedDeploymentPatches,
		},
	} {
		mutateOpts := Options{}
		mutateOpts.DefaultInstance = "my-gcp-project-42:europe-west1:sql-master"
		mut := Mutate(mutateOpts)

		ar := mut(data.review)
		require.NotNil(t, ar)

		assert.Equal(t, data.allowed, ar.Allowed)
		if data.allowed {
			assert.Equal(t, v1beta1.PatchTypeJSONPatch, *ar.PatchType)
			assert.NotEmpty(t, ar.Patch)
			// FIXME This string comparison might not be really exact, we need a more elegant way
			assert.Equal(t, data.expectedPatches, string(ar.Patch))
		}
	}

}
