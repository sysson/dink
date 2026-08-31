BUF ?= buf
GO  ?= go
GOLANGLINT ?= golangci-lint
TILT ?= tilt
KUBECTL ?= kubectl
MINI ?= minikube

IMAGE ?= ghcr.io/sysson/dink
TAG ?= dev
MINIKUBE_PROFILE ?= dink-dev
NAMESPACE ?= dink-system

.PHONY: all build test generate lint clean fix dev image load certs deploy undeploy logs docker-env start

all: build

build:
	$(GO) build ./...

test:
	$(GO) test -race ./...

## generate: Re-generate protobuf + gRPC stubs from proto/ into sdk/*/gen/
generate:
	$(BUF) generate

## lint: Run golangci-lint and buf lint
lint:
	$(BUF) lint
	$(GOLANGLINT) run ./...

clean:
	$(GO) clean ./...

fix:
	$(GO) fix ./...

start:
	$(MINI) start -p $(MINIKUBE_PROFILE)

## dev: Run the Tilt dev loop against the local minikube cluster
dev: start
	$(TILT) up

## image: Build the release container image
image:
	docker build -t $(IMAGE):$(TAG) .

## load: Push the image into the minikube node's image store
load: image
	minikube -p $(MINIKUBE_PROFILE) image load $(IMAGE):$(TAG)

## certs: Generate the CA/server/client certs and apply the dink-tls Secret
certs:
	./deploy/gen-certs.sh

## deploy: Apply the manifests (expects `make load certs` first)
deploy:
	$(KUBECTL) apply -f deploy/dink.yaml
	$(KUBECTL) -n $(NAMESPACE) rollout status deployment/dink --timeout=120s

## undeploy: Remove the manifests from the cluster
undeploy:
	$(KUBECTL) delete -f deploy/dink.yaml --ignore-not-found

## logs: Tail the dink control plane logs
logs:
	$(KUBECTL) -n $(NAMESPACE) logs -l app.kubernetes.io/name=dink -f --tail=100

## docker-env: Print the env needed to point a local docker CLI at dink
docker-env:
	@echo 'kubectl -n $(NAMESPACE) port-forward svc/dink 2376:2376 &'
	@echo 'export DOCKER_HOST=tcp://localhost:2376'
	@echo 'export DOCKER_TLS_VERIFY=1'
	@echo 'export DOCKER_CERT_PATH=$(CURDIR)/.certs/docker'
