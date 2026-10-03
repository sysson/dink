# Dink: Docker API for Kubernetes

Dink lets Docker clients manage workloads on a Kubernetes cluster through a
Docker Engine-compatible API. It translates supported Docker operations into
Kubernetes resources, so the Docker CLI, Compose, Portainer, and Docker SDK
clients can connect without speaking to the Kubernetes API directly.

Dink is a translation layer, not a Docker daemon: containers run as Pods, and
some Docker behavior has no direct Kubernetes equivalent. Network isolation,
storage, image handling, and supported API behavior are described in the
[translation contract](docs/docker-translation-contract.md) and
[endpoint status tracker](docs/docker-api-conformance.md).

## Components

| Component | Role |
| --- | --- |
| `dink` | Docker-compatible HTTP API and Kubernetes control plane. Docker workloads are scoped to the namespace identified by the client certificate. |
| `dinki` | Tenant OCI registry used for image pulls and registry operations. Its data and metadata are stored on a persistent volume. |
| `dinkle` | Operator CLI for the certificate authority, tenant namespaces, client certificates, plugins, and secrets. |
| `sdk/` | Go SDK for Dink auth, secrets, and volume plugins. |

## How It Works

| Docker concept | Kubernetes implementation |
| --- | --- |
| Container | Deployment and Pod (or a short-lived Job for `--rm`) |
| Docker network | Tenant-scoped logical network and NetworkPolicies |
| Docker volume | PersistentVolumeClaim |
| Swarm service and task | Deployment and Pod |
| Swarm secret and config | Kubernetes Secret and ConfigMap |
| Docker image registry | `dinki`, backed by persistent storage |

`dink` authenticates Docker clients with mutual TLS. A client certificate
identifies its tenant namespace; `dinkle` creates that namespace and issues its
client certificate. Docker API and registry endpoints use TLS. The deployment
manifests are in [`deploy/`](deploy/).

## Docker API Coverage

Dink supports container lifecycle, logs and attached exec; logical networks and
PVC-backed volumes; tenant-registry image pulls and inspection; and a partial
Swarm API for services, tasks, secrets, and configs. `GET /info` advertises the
Kubernetes cluster as an active Swarm, so Portainer may display Dink as a Swarm
environment. Swarm membership and node administration still belong to
Kubernetes tooling.

This is not a complete Docker Engine implementation. Tagged Docker-driver builds
can use an optional external BuildKit backend; see the
[BuildKit integration](docs/buildkit.md) for configuration and first-pass limits.
Image load and export are not implemented; Docker plugin inventory is limited to read-only
views of Dink plugin registrations, and Docker plugin lifecycle operations are
unsupported. Other routes may support only part of Docker's options or
semantics. See the [endpoint status tracker](docs/docker-api-conformance.md)
for route-level details; it is a source audit, not an external conformance-test
result.

Docker/Compose labels can explicitly add Kubernetes Pod labels and annotations
using `dink.io/pod-label/<key>` and `dink.io/pod-annotation/<key>`. Dink validates
the metadata and protects its internal keys. This supports user-managed logging
collectors and other metadata-driven integrations without managing them itself;
see [Pod labels and annotations](docs/docker-translation-contract.md#pod-labels-and-annotations).

## Production Deployment

The included manifests are a starting point for a cluster installation. They
keep the Docker API and registry Services internal to the cluster; arrange
private network access separately. Before deploying, review the RBAC and
host-level access in the manifests for your cluster and threat model.

### Requirements

- A Kubernetes cluster and a `kubectl` context with permission to install the
	included CRDs, namespaces, cluster RBAC, workloads, and Secrets.
- `curl` and `tar` to install the released `dinkle` utility on Linux or macOS.
- A default StorageClass for the `dinki` registry PVC. Tenant-created volumes
	also depend on cluster storage.
- A CNI that enforces Kubernetes NetworkPolicies if Docker network isolation is
	required. Metrics Server is needed for nonzero container stats.
- Public access to `ghcr.io/sysson/dink` and `ghcr.io/sysson/dinki`, or image
	pull credentials configured for the cluster.

The `dinki-node` DaemonSet configures each node's containerd registry trust for
`dinki.io`. It uses host networking, runs as root, and mounts
`/etc/containerd/certs.d`; the node's containerd must have that directory
configured as its registry `config_path`. Review this integration before
installing it on production nodes.

### Install

Choose a published beta release. Its Git tag pins the deployment manifests and
its release assets include the `dinkle` binary. The examples below use Linux
amd64; release archives are also published for Linux, macOS, and Windows on
amd64 and arm64.

```sh
DINK_VERSION=v0.1.0-beta.1
mkdir -p "$HOME/.local/bin"
curl -fsSL "https://github.com/sysson/dink/releases/download/${DINK_VERSION}/dinkle_linux_amd64.tar.gz" \
  | tar -xz -C "$HOME/.local/bin"
export PATH="$HOME/.local/bin:$PATH"

dinkle --help
```

Use `DINK_MANIFEST_REF=main` for the rolling beta manifest, or set it to a
version tag such as `v0.1.0-beta.1` for a pinned release. `DINK_VERSION`
selects the `dinkle` binary release.

Select the target cluster and create the CA and server TLS Secrets before
applying the workloads. The CA private key is cached locally and stored in the
cluster as a Secret; protect the local directory and restrict access to the
Secret:

```sh
kubectl config use-context <production-context>
export DINK_CERTS="$HOME/.config/dink/.certs"
dinkle --certsDir "$DINK_CERTS" ca generate
dinkle --certsDir "$DINK_CERTS" server issue --dnsName docker-api.example.com
dinkle --certsDir "$DINK_CERTS" server issue \
  --serviceName dinki --serverSecretName dinki-tls
```

Apply the exact release's Kustomize configuration directly from GitHub:

```sh
DINK_MANIFEST_REF="${DINK_MANIFEST_REF:-$DINK_VERSION}"
kubectl apply -k "https://github.com/sysson/dink//deploy?ref=${DINK_MANIFEST_REF}"
kubectl -n dink-system rollout status deployment/dink
kubectl -n dink-system rollout status deployment/dinki
kubectl -n dink-system rollout status daemonset/dinki-node
```

Configure a private load balancer or TCP/TLS-passthrough route to
`dink-system/dink:2376`; the client TLS connection must reach Dink unchanged
for mutual TLS to work. The manifests do not configure external ingress or
load balancing.

Create a tenant and its default client certificate. Use the certificate files
from the operator's protected `DINK_CERTS` directory to configure a Docker
context on a trusted client machine:

```sh
dinkle --certsDir "$DINK_CERTS" tenant create team-a

docker context create dink-team-a --docker \
	"host=tcp://docker-api.example.com:2376,ca=$DINK_CERTS/ca.pem,cert=$DINK_CERTS/team-a/default/cert.pem,key=$DINK_CERTS/team-a/default/key.pem"
docker --context dink-team-a info
```

Create a separate client certificate with `dinkle client create team-a <client>`
for each Docker client or operator that should have independent credentials.
Protect each client key and only distribute the credentials to that client.

## Local Development

The development loop uses Minikube, Docker, Tilt, `kubectl`, Go 1.27.1 or
newer, and Make. `make bootstrap` starts the configured Minikube profile,
creates the local CA and server certificates, and creates the `dev` tenant.
Then start Tilt:

```sh
make bootstrap
make dev
```

Tilt builds and loads the `dink` and `dinki` images into Minikube, applies the
Kubernetes resources, and forwards the Docker API and registry ports. The Make
targets create and select a Docker context named `dink`:

```sh
docker --context dink info
docker --context dink run --rm hello-world
```

`tilt down` stops the dev loop. The Minikube cluster remains available. Useful
project checks are:

```sh
make build
make test
make lint
```

## Configuration and Operations

- [`deploy/config.json`](deploy/config.json) configures the Docker API, TLS,
	logging, Kubernetes namespace defaults, and container resource defaults.
- [`deploy/dinki-config.json`](deploy/dinki-config.json) configures registry
	TLS, storage paths, logging, and the internal API client identity. `storage.path`
	selects the local blob directory and `metadata.path` selects the bbolt database;
- [`Makefile`](Makefile) contains local cluster, certificate, tenant, context,
	and deployment targets. Run `make` to see the default build target, or inspect
	the file for available targets.
- The `dinkle` CLI manages CA certificates, tenants, client credentials,
	plugins, and secrets. Use `go run ./cmd/dinkle --help` for its commands.

The `dink` and `dinki` Deployments each run a single replica in the supplied
manifests. `dinki` uses a `ReadWriteOnce` PVC and a `Recreate` update strategy;
plan backups and recovery for its persistent data. These manifests do not
provide high availability or a production backup policy.

## Limitations

- Docker API coverage is incomplete and some supported operations have
	Kubernetes-specific behavior. Check the [endpoint status tracker](docs/docker-api-conformance.md)
	before depending on an endpoint.
- Tagged, single-platform `docker build` and `docker buildx build` operations
	using the Docker driver are supported through an optional external BuildKit
	backend. Builds are disabled when `buildKit.url` is unset; the legacy
	`POST /build` endpoint and Docker's node-local image store are not implemented.
	See [BuildKit integration](docs/buildkit.md) for setup and supported behavior.
- Network isolation depends on CNI NetworkPolicy enforcement. `--network host`
	bypasses NetworkPolicies.
- Container stats depend on Metrics Server; PVC capacity is not the same as
	bytes used.
- Docker host paths, process/runtime details, and other host-specific features
	do not map directly to Kubernetes.

## References

- [Docker API endpoint status](docs/docker-api-conformance.md)
- [Docker translation contract](docs/docker-translation-contract.md)
- [Portainer D2K](https://github.com/portainer/d2k)
