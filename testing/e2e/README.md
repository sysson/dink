# Local end-to-end testing

Run from the repository root:

```sh
make test-e2e
```

This uses a **new disposable cluster**, not the development or production
cluster. It builds both service images from the current checkout, loads them
into Kind, provisions certificates and two tenants using the current
`dinkle`, and deploys the normal manifests with a temporary image-tag overlay.
Published Dink images are not used.

## Devcontainer or host?

The repository's devcontainer is the recommended environment: its
Docker-in-Docker daemon can run Kind directly. There is no need to run this on
the host, stop Tilt, or select a different active context.
The runner maps test-only TLS NodePorts to localhost, avoiding kubectl
port-forward interruptions during streaming operations. The loopback IP is
included in the generated server certificate.

Clusters use Kind's normal Docker network without an MTU override. Calico
detects the node interface MTU when configuring Pod networking. The runner
does not change Docker network or daemon settings.

A Linux host with the same tools also works. Docker-socket-mounted containers
are a different setup: the cluster API and port mappings created by the Docker
host must be reachable from the container. If they are not, run the harness on
the host instead. Other host operating systems have not been validated.

Requirements:

- Bash, Go matching `go.mod`, git, Docker, Kind and kubectl.
- Docker Compose and Buildx CLI plugins installed in a standard system plugin
  directory (the runner uses a fresh Docker configuration).
- A reachable real Docker daemon, approximately 4 GiB of spare RAM for the
  cluster, plus resources for Go/image builds, and available disk space.
- Internet access for Kubernetes, Calico v3.33.0, BuildKit and base images, Go
  dependencies, and the pinned upstream Docker CLI checkout. No registry login
  is needed.

The runner isolates `KUBECONFIG`, Docker configuration, and certificate
storage. It does not select contexts in your normal configuration,
use existing tenant credentials, or prune the Docker daemon. Locally built
image layers remain cached on the real Docker daemon.

## Coverage

- BuildKit's existing build/load/local-export/history tests, including image
  and history isolation between tenants.
- BuildKit backend mTLS: valid connection, wrong server identity rejection,
  and missing client certificate rejection.
- Multi-tag upstream push to a disposable authenticated TLS registry, comparing
  upstream digests with the retained Dinki images and rejecting unauthenticated
  pushes. The runner generates temporary credentials, logs in using only its
  isolated Docker configuration, and removes the registry with the cluster.
- Buildx Docker-driver build followed by actually running the built image.
- Container lifecycle, logs, exec, cross-tenant inspect rejection, and ensuring
  cross-tenant force removal leaves the owning tenant's container running.
  Rename verifies the Docker ID, Deployment UID/spec, and running Pod UID stay
  unchanged, and that exec works under the new Docker name.
- Compose service-name communication, named volume persistence across
  recreation, and teardown. Exec explicitly disables stdin and TTY because the
  harness is noninteractive. Force-recreation exercises Compose's stopped
  temporary container, old-container stop/removal, and rename/start sequence.
- Swarm-compatible replicated service creation and scaling. This does not
  initialize a Docker Swarm.
- Attachable overlay network creation and inspection, verifying that the CRD
  preserves the attachable flag.
- Selected unmodified Docker CLI upstream tests:
  `TestContainerRename`, `TestContainerRenameEmptyOldName`,
  `TestCreateWithEmptySourceVolume`, and `TestCreateWithEmptyVolumeSpec`.

The CLI checkout is pinned to
`75d1d1a9e62f89d8284ad0b4b8678b936ae5bed9`. Tests invoke the installed Docker
CLI, not a CLI built from that checkout. The upstream JSON report must show all
four selected tests passing; missing/skipped tests fail the harness.
Its `vendor.mod`/`vendor.sum` are copied to `go.mod`/`go.sum` in the temporary
checkout so tests use the upstream vendored dependencies.
The selection needs Alpine at the upstream fixture name
`registry:5000/alpine:frozen`; the runner pulls Alpine 3.21 through Dink and tags
it with that name in the test tenant registry. No auxiliary upstream registry
is needed for these selected cases.
`TestRunAttachedFromRemoteImageAndRemove` is deliberately not selected: it
skips remote daemons and requires an auxiliary registry.

Calico v3.33.0 enforces NetworkPolicies, and Kind provides local PVC
provisioning. The runner configures each Kind node's containerd registry trust
directory and verifies it before deployment. It also waits for the Calico and
CoreDNS rollouts; an unsuccessful readiness check fails the harness rather
than being ignored. Kind maps the test-only registry, Dink API, and BuildKit
NodePorts to localhost on the devcontainer.
External LoadBalancer access, public registry pushes, full Docker
conformance, multi-node scheduling, and explicit network-isolation denial
tests are not covered by this initial suite.

## Configuration

```sh
# Use another local Docker daemon context.
E2E_HOST_CONTEXT=my-docker-context make test-e2e

# Skip downloading/running upstream tests while developing local scenarios.
E2E_UPSTREAM=0 make test-e2e

# Adjust per-node resource limits and Kubernetes version.
E2E_MEMORY=6144 E2E_CPUS=4 E2E_KUBERNETES_VERSION=v1.37.0 make test-e2e

```

Run `bash testing/e2e/run.sh --help` for the settings.

The runner uses bounded readiness waits, scenario command timeouts, and Go test
timeouts. Docker command failures report context cancellation or deadline
expiration explicitly rather than only the resulting killed-process error.
It preserves the failure exit code, gathers diagnostics, and tears down on
normal exit,
failure, or interrupt. An uncatchable termination (such as SIGKILL) cannot run
cleanup. Ctrl+C during startup runs cleanup too; logging stays alive and
additional interrupts are ignored while teardown completes. If cluster deletion
fails, state is retained and a retry command is printed.

Logs are retained in the temporary directory printed at startup and exit:
runner output, Kubernetes workload descriptions/events,
service logs, containerd configuration, and upstream JSON test results.
Certificates, Docker credentials/configuration, and the temporary upstream
checkout are removed after cluster deletion. Diagnostics do not dump Secret
objects, but application logs/workload descriptions can still contain data
from test workloads; review them before sharing.

These tests are opt-in; ordinary `make test` skips the real-setup tests.
`make test-e2e` is the single entry point, including all BuildKit integration
tests. No manually configured backend endpoint, registry account, port-forward,
or development deployment is required. The test-only BuildKit and registry
NodePorts are created solely in the disposable cluster.

Each scenario runs once, sequentially, and its elapsed time includes cleanup.
Long-running local fixtures trap SIGTERM, terminate their child, and wait for
it, avoiding Kubernetes' default 30-second shutdown grace expiry. Production
shutdown grace is unchanged. Unrelated containers can stop concurrently;
operations on the same container remain serialized by workload identity.
The unmodified upstream fixtures may still take the full shutdown grace.

Scenario failures do not suppress the upstream CLI phase. A detected product
bug is a failing test, not an automatic skip or expected failure.

## Compatibility regressions

The suite exercises two compatibility gaps found during initial validation:

- Compose's generated container-name network alias must reuse its own primary
  DNS Service, rather than collide with it.
- Upstream `TestContainerRename` starts with a Docker-valid name containing
  underscores and uppercase letters. Creation and rename must both succeed
  without using the Docker-visible name as a Kubernetes resource name.

The running-container scenario separately verifies rename does not replace
the workload or Pod or alter the Deployment spec. Kubernetes can advance a
Deployment's generation for a metadata annotation change, so generation alone
is not used as a proxy for a spec change.
