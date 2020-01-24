# SQLBee

SQLBee pollinates your unsuspecting pods with cloud sql proxy sidecars
so your services can easily connect to your cloud sql instance.
When using [GCP Cloud SQL](https://cloud.google.com/sql/docs/) it is recommended to use
[cloudsql-proxy](https://github.com/GoogleCloudPlatform/cloudsql-proxy) as a sidecar in your pods.
Of course you can add this sidecar manually to all your pods requiting database access, but this
is a tedious and possibly error prone (outdated images, typos etc.) process. SQLBee simply injects
the same sidecar into all your pods. In case you some of your pods are little different than others
you can customize the injection via annotations.

## Usage

Deploy SQLBee (docker.io/connctd/sqlbee) as a mutating admission webhook 
(see https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/).
In the deployment folder an example configuration is provided. You will need have secret with
a credentials.json with permissions for Cloud SQL to be used by the sidecar. Other authentication
aren't currently supported You can either use the same secret everywhere or customize it per pod
via annotations.

Depending on whether you set the `annotationRequired` parameter you either need to add the annotation
`sqlbee.connctd.io.inject: "true"` to your pod specifications or you need to add nothing at all
to inject your pods with a cloud-sql-proxy sidecar.

### Command line arguments

| Name | Default value | Description | Required |
| ---- | ------------- | ----------- | ---------|
| cert | none          | Path to the server certificate to be used | yes |
| key  | none          | Path to the servers private key | yes |
| instance | none      | Name of the default cloud sql instance if not specified via annotation | no |
| secret | none | Name of a secret containing the GCP credentials for this cloud-sql-proxy | no |
| ca-map | none | Name of a config map containing root certificates | no |
| annotationRequired | false | Whether to only inject the sidecar if the annotation is present | no |
| loglevel | info | The log level | no |

### Annotations

| Name | Description | Required |
| ---- | ----------- | -------- |
| sqlbee.connctd.io.inject | Wether to inject with a cloud-sql-proxy | no |
| sqlbee.connctd.io.image | Image to be used, default gcr.io/cloudsql-docker/gce-proxy:1.13 | no |
| sqlbee.connctd.io.instance | cloud-sql instance to connect to, required if no default is set | maybe |
| sqlbee.connctd.io.secret | Secret containing credentials | no |
| sqlbee.connctd.io.caMap | Config map containing root certificates | no | 
| sqlbee.connctd.io.cpuRequest | value of the sidecar cpu request, defaults to "30m" | no | 
| sqlbee.connctd.io.memRequest | value of the sidecar memory request, defaults to "50Mi" | no |


