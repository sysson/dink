# Dink: Docker API for Kubernetes

Dink lets the Docker CLI, Compose, Portainer, and Docker SDK clients manage
workloads on Kubernetes through a Docker Engine-compatible API.

Containers become Deployments or Jobs, volumes become PersistentVolumeClaims,
and networks use Kubernetes NetworkPolicies. Client certificates identify tenant
namespaces. Dink is a translation layer, not a Docker daemon: API coverage is
incomplete and some operations have Kubernetes-specific behavior. See the
[API coverage](docs/docker-api-conformance.md) and
[translation contract](docs/docker-translation-contract.md).

| Component | Role |
| --- | --- |
| `dink` | Docker-compatible API and Kubernetes control plane. |
| `dinki` | Tenant OCI registry backed by persistent storage. |
| `dinkle` | Operator CLI for installation, upgrades, certificates, and tenants. |
| `sdk/` | Go SDK for auth, secret, and volume plugins. |

## Install

You need:

- A Kubernetes cluster, `kubectl`, and permission to install cluster-wide resources.
- A default StorageClass and a CNI that enforces NetworkPolicies.
- Containerd configured to use `/etc/containerd/certs.d` for registry trust.
- Node access to the image registries, or configured image-pull credentials.

**Review the release manifests before installing.** They include cluster-wide
RBAC, a host-mounted registry DaemonSet, and a privileged BuildKit backend.
Services are internal; you must arrange private API access separately. See the
[installation guide](docs/installation.md) for requirements and customization.

### Download Dinkle

Choose a [published release](https://github.com/sysson/dink/releases) containing
the installation assets. Set `DINK_VERSION` to its tag. This example is for Linux
amd64; archives are also available for Linux, macOS, and Windows on amd64/arm64.

```sh
export DINK_VERSION="vX.Y.Z" # Replace with the selected release tag.
mkdir -p "$HOME/.local/bin"
curl -fsSL "https://github.com/sysson/dink/releases/download/${DINK_VERSION}/dinkle_linux_amd64.tar.gz" \
  | tar -xz -C "$HOME/.local/bin"
export PATH="$HOME/.local/bin:$PATH"
dinkle --version
```

### Deploy and connect

Select your cluster and install the release matching your Dinkle binary:

```sh
kubectl config use-context <cluster-context>
export DINK_CERTS="$HOME/.config/dink/.certs"
dinkle --certsDir "$DINK_CERTS" install --yes --dnsName docker-api.example.com
```

Provide a private TCP/TLS-passthrough route from `docker-api.example.com:2376`
to `dink-system/dink:2376`, preserving mutual TLS. Protect the certificate
directory: it contains the CA private key.

Create a tenant and configure its Docker context:

```sh
dinkle --certsDir "$DINK_CERTS" tenant create team-a
docker context create dink-team-a --docker \
  "host=tcp://docker-api.example.com:2376,ca=$DINK_CERTS/ca.pem,cert=$DINK_CERTS/team-a/default/cert.pem,key=$DINK_CERTS/team-a/default/key.pem"
docker --context dink-team-a info
docker --context dink-team-a run --rm hello-world
```

For clients on other machines, distribute only their client credentials and CA
certificate, never the CA private key.

### Upgrade

Download the desired release's Dinkle binary, then run:

```sh
dinkle upgrade --yes
```

Upgrades preserve certificates and apply the standard release configuration.
For customized installations, use your deployment tooling instead; see
[upgrades and customization](docs/installation.md#upgrades-and-customization).

## Development

Open the repository in its VS Code devcontainer for the included Go, Docker,
Kind, kubectl, and Tilt tooling. Outside the devcontainer, install those tools
plus Make, Buf, and golangci-lint; use the Go version specified in
[go.mod](go.mod).

Start the local Kind/Tilt development environment:

```sh
make bootstrap
make dev
```

Tilt builds and loads the service images, applies resources, and forwards ports.
The setup creates a tenant and Docker context named `dink`:

```sh
docker --context dink info
docker --context dink run --rm hello-world
```

Stop Tilt with Ctrl-C; use `tilt down` to remove its deployed resources. The Kind
cluster remains available.

```sh
make build
make test
make lint
make test-e2e
```

See the [E2E guide](testing/e2e/README.md) for disposable-cluster tests and the
[Makefile](Makefile) for additional development commands.

## Documentation

- [Installation, configuration, and operations](docs/installation.md)
- [Docker API coverage](docs/docker-api-conformance.md)
- [Docker translation contract](docs/docker-translation-contract.md)
- [BuildKit integration](docs/buildkit.md)
- [Release maintenance and recovery](docs/releases.md)
- [Standalone Tini image](images/tini/README.md)
