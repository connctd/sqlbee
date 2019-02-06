# SQLBee

sqlbee pollinates your unsuspecting pods and deployments with cloud sql proxy sidecars
so your services can easily connect to your cloud sql instance

## Usage

### Command line arguments

| Name | Default value | Description | Required |
| ---- | ------------- | ----------- | ---------|
| cert | none          | Path to the server certificate to be used | yes |
| key  | none          | Path to the servers private key | yes |
| instance | none      | Name of the default cloud sql instance if not specified via annotation | no |
| secret | none | Name of a secret containing the GCP credentials for this cloud-sql-proxy | no |
| ca-map | none | Name of a config map containing root certificates | no |

### Annotations

| Name | Description | Required |
| ---- | ----------- | -------- |
| sqlbee.connctd.io.inject | Wether to inject with a cloud-sql-proxy | no |
| sqlbee.connctd.io.image | Image to be used | no |
| sqlbee.connctd.io.instance | cloud-sql instance to connect to, required if no default is set | maybe |
| sqlbee.connctd.io.secret | Secret containing credentials | no |
| sqlbee.connctd.io.caMap | Config map containing root certificates | no | 
