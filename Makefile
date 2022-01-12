BINARY_NAME=sqlbee
GO_ENV=GO111MODULE=on
DOCKER_IMAGE=eu.gcr.io/molten-mariner-162315/connctd/sqlbee

VERSION 		?= $(shell git describe --tags --always --dirty)
RELEASE_VERSION	?= $(shell git describe --abbrev=0)
LDFLAGS       	?= -X github.com/connctd/sqlbee/pkg/sting.Version=$(VERSION) -w -s

GO_BUILD=$(GO_ENV) GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -mod=vendor -ldflags "$(LDFLAGS)"
GO_TEST=$(GO_ENV) go test -mod=vendor -v

.PHONY: clean test docker
.DEFAULT_GOAL := build

build: $(BINARY_NAME)

$(BINARY_NAME):
	$(GO_BUILD) -o $(BINARY_NAME) ./cmd/sqlbee

docker: test build
	docker build . -t $(DOCKER_IMAGE):$(VERSION)

test:
	$(GO_TEST) ./...

clean:
	rm -f sqlbee

dist.push: docker
	docker push $(DOCKER_IMAGE):$(VERSION)
