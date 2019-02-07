package main

import (
	"flag"

	"github.com/sirupsen/logrus"

	"github.com/connctd/sqlbee/pkg/sting"
)

var (
	certPath          = flag.String("cert", "", "Path to server certificate")
	keyPath           = flag.String("key", "", "Path to server private key")
	instanceName      = flag.String("instance", "", "Default cloud sql instance to connect to")
	secretName        = flag.String("secret", "", "Optional secret to use for credentials. Needs to contain a valid 'credentials.json' key")
	caConfigMapName   = flag.String("ca-map", "", "Optional name of a config map containing root certs")
	requireAnnotation = flag.Bool("annotationRequired", false, "If set, the inject annotation is required to inject the object")
)

func main() {
	logrus.WithFields(logrus.Fields{
		"version": sting.Version,
	}).Info("Starting SQLBee")
	flag.Parse()

	opts := sting.NewOptions()

	mutateOpts := Options{}
	mutateOpts.DefaultInstance = *instanceName
	mutateOpts.DefaultCertVolume = *caConfigMapName
	mutateOpts.DefaultSecretName = *secretName
	mutateOpts.RequireAnnotation = *requireAnnotation

	opts.Mutate = Mutate(mutateOpts)
	opts.CertFile = *certPath
	opts.KeyFile = *keyPath

	server, err := sting.New(opts)
	if err != nil {
		logrus.WithError(err).Panic("Failed to create inject server")
	}
	sting.Main(server)
}
