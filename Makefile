BUF ?= buf
GO  ?= go
GOLANGLINT ?= golangci-lint
TILT ?= tilt
KUBECTL ?= kubectl
MINI ?= minikube
DOCKER ?= docker

IMAGE ?= ghcr.io/sysson/dink
TAG ?= dev
MINIKUBE_PROFILE ?= dink-dev
NAMESPACE ?= dink-system
DINK_CONFIG ?= $(HOME)/.config/dink
CERT_DIR ?= $(DINK_CONFIG)/.certs
DINKLE ?= ./cmd/dinkle
TENANT ?= dev
CLIENT ?= default
DOCKER_CTX ?= dink
DINK_HOST ?= tcp://localhost:2376
DINK_PORT ?= 2376

# Image reference to build; tilt overrides this with the tag it expects.
REF ?= $(IMAGE):$(TAG)

DOCKER_CERT_DIR := $(CERT_DIR)/$(TENANT)/$(CLIENT)
CTX_ENDPOINT := host=$(DINK_HOST),ca=$(DOCKER_CERT_DIR)/ca.pem,cert=$(DOCKER_CERT_DIR)/cert.pem,key=$(DOCKER_CERT_DIR)/key.pem

# minikube's docker driver runs the cluster node as a container on the host
# daemon, so anything touching minikube must bypass the dink context. 
HOST_DOCKER := env -u DOCKER_HOST -u DOCKER_TLS_VERIFY -u DOCKER_CERT_PATH DOCKER_CONTEXT=minikube

.PHONY: all build test generate lint clean fix dev image image-minikube load ca-generate ca-generate-local ca-rotate server tenant bootstrap \
	context context-sync context-use context-default context-rm port-forward restart release show-image \
	deploy undeploy logs docker-env start

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
	@$(HOST_DOCKER) $(MINI) status -p $(MINIKUBE_PROFILE) >/dev/null 2>&1 && echo "minikube profile '$(MINIKUBE_PROFILE)' already running" || $(HOST_DOCKER) $(MINI) start -p $(MINIKUBE_PROFILE)

## dev: Run the Tilt dev loop against the local minikube cluster
dev: start context-use
	$(TILT) up

## image: Build the release container image in docker
image:
	$(HOST_DOCKER) $(DOCKER) build --target $(TARGET) -t $(REF) .

## load: Push the image into the minikube node's image store
load: image
	$(HOST_DOCKER) $(MINI) -p $(MINIKUBE_PROFILE) image load $(REF)

## image-minikube: Build straight into the minikube node's image store; override REF to retag
image-minikube:
	$(HOST_DOCKER) $(MINI) -p $(MINIKUBE_PROFILE) image build -t $(REF) .

## ca-generate: Create the CA/namespaces/server cert if they don't exist yet, and apply the CA + dink-tls Secrets
# Idempotent: reuses the cached (or cluster) CA and only (re)issues the server cert, so it's
# safe to call on every devcontainer start or CI run.
ca-generate:
	$(GO) run $(DINKLE) --certsDir $(CERT_DIR) ca generate --systemNamespace $(NAMESPACE)

## ca-generate-local: Same as ca-generate but without touching the cluster
ca-generate-local:
	$(GO) run $(DINKLE) --certsDir $(CERT_DIR) ca generate --systemNamespace $(NAMESPACE) --apply=false

## ca-rotate: Rotate the CA and reissue the server cert, invalidating existing clients
ca-rotate:
	$(GO) run $(DINKLE) --certsDir $(CERT_DIR) ca rotate --systemNamespace $(NAMESPACE) --yes
	@echo "the pod still serves the previous certificate; run 'make restart'" >&2

## server: Issue the server certificate from the existing CA and apply it as the dink-tls Secret
server:
	$(GO) run $(DINKLE) --certsDir $(CERT_DIR) server issue --systemNamespace $(NAMESPACE)

## tenant: Create the '$(TENANT)' tenant and refresh the local docker context for it
tenant:
	$(GO) run $(DINKLE) --certsDir $(CERT_DIR) tenant create $(TENANT)
	@$(MAKE) --no-print-directory context-sync

## bootstrap: Bring up the cluster and create the CA, server certificate and '$(TENANT)' tenant if missing
# Safe to re-run: `ca generate` reuses an existing CA, and the server certificate and
# tenant are only created when they aren't there already.
bootstrap:
	@$(MAKE) --no-print-directory start
	@$(MAKE) --no-print-directory ca-generate
	@if [ -f "$(CERT_DIR)/tls.crt" ]; then \
		echo "server certificate already present in $(CERT_DIR); skipping"; \
	else \
		$(MAKE) --no-print-directory server; \
	fi
	@if $(KUBECTL) get namespace $(TENANT) >/dev/null 2>&1; then \
		echo "tenant '$(TENANT)' already exists; refreshing docker context"; \
		$(MAKE) --no-print-directory context-sync; \
	else \
		$(MAKE) --no-print-directory tenant; \
	fi

## restart: Roll the dink deployment so it picks up a new TLS Secret
restart:
	$(KUBECTL) -n $(NAMESPACE) rollout restart deployment/dink
	$(KUBECTL) -n $(NAMESPACE) rollout status deployment/dink --timeout=120s

## deploy: Apply the manifests (expects `make load ca-generate` first)
deploy:
	$(KUBECTL) apply -k deploy
	$(KUBECTL) -n $(NAMESPACE) rollout status deployment/dink --timeout=120s

## undeploy: Remove the manifests from the cluster
undeploy:
	$(KUBECTL) delete -k deploy --ignore-not-found

## logs: Tail the dink control plane logs
logs:
	$(KUBECTL) -n $(NAMESPACE) logs -l app.kubernetes.io/name=dink -f --tail=100

## context: Create or update the '$(DOCKER_CTX)' docker context pointing at dink
# A missing local client certificate is restored from the tenant's cluster
# Secret first, so a rebuilt devcontainer can reattach without reissuing.
context:
	@if [ ! -f $(DOCKER_CERT_DIR)/cert.pem ]; then \
		echo "no local client certificate in $(DOCKER_CERT_DIR); restoring from cluster secret $(TENANT)/dink-client-$(CLIENT)"; \
		$(GO) run $(DINKLE) --certsDir $(CERT_DIR) client sync $(TENANT) $(CLIENT) || { \
			echo "no client certificate in $(DOCKER_CERT_DIR); run 'make tenant' first" >&2; exit 1; }; \
	fi
	@if $(HOST_DOCKER) $(DOCKER) context inspect $(DOCKER_CTX) >/dev/null 2>&1; then \
		$(HOST_DOCKER) $(DOCKER) context update $(DOCKER_CTX) --docker "$(CTX_ENDPOINT)" >/dev/null; \
		echo "updated docker context $(DOCKER_CTX)"; \
	else \
		$(HOST_DOCKER) $(DOCKER) context create $(DOCKER_CTX) \
			--description "dink control plane ($(NAMESPACE))" \
			--docker "$(CTX_ENDPOINT)" >/dev/null; \
		echo "created docker context $(DOCKER_CTX)"; \
	fi

# The context stores a copy of the client certificate, not a reference to it, so
# it has to be refreshed every time the leaves are reissued.
context-sync:
	@if command -v $(DOCKER) >/dev/null 2>&1; then \
		$(MAKE) --no-print-directory context; \
	else \
		echo "docker CLI not found; skipping docker context refresh" >&2; \
	fi

## context-use: Point the local docker CLI at dink
context-use: context
	@$(HOST_DOCKER) $(DOCKER) context use $(DOCKER_CTX) >/dev/null
	@echo "docker context set to $(DOCKER_CTX); it needs 'make port-forward' unless tilt is running"

## context-default: Point the local docker CLI back at the host daemon
context-default:
	@$(HOST_DOCKER) $(DOCKER) context use default >/dev/null
	@echo "docker context set to default"

## context-rm: Remove the dink docker context
context-rm: context-default
	@$(HOST_DOCKER) $(DOCKER) context rm $(DOCKER_CTX) >/dev/null 2>&1 || true

## port-forward: Tunnel the dink API to $(DINK_HOST) in the foreground
port-forward:
	$(KUBECTL) -n $(NAMESPACE) port-forward svc/dink $(DINK_PORT):2376

## docker-env: Print the env needed to point a one-off docker command at dink
docker-env:
	@echo '# preferred: make context-use'
	@echo '# or, for a single shell:'
	@echo 'export DOCKER_HOST=$(DINK_HOST)'
	@echo 'export DOCKER_TLS_VERIFY=1'
	@echo 'export DOCKER_CERT_PATH=$(DOCKER_CERT_DIR)'
