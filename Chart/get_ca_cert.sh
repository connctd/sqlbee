#!/bin/sh
K8S_CA_CERT=$(kubectl config view --raw --minify --flatten -o jsonpath='{.clusters[].cluster.certificate-authority-data}')

if [ -f "values.yaml" ]; then
  echo "Patching values.yaml"
  sed -i "s/CA_BUNDLE/$K8S_CA_CERT/g" values.yaml
else
  echo "values.yaml not found, please set the value webhook.caBundle manually to"
  echo $K8S_CA_CERT
fi
