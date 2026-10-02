# External BuildKit builds

Dink can delegate tagged, single-platform Docker builds to an operator-managed,
stock BuildKit daemon. The daemon can run in a separate Kubernetes Pod; Dink
does not create the Pod, select the Buildx Kubernetes driver, or use the node's
container runtime image store.

The client continues using the **Docker driver**:

```text
Docker CLI / Buildx -> Dink -> stock buildkitd -> Dinki
```

Both Dink and Dinki must run versions containing the build integration. Leaving
`buildKit.url` empty disables builds.

## Configuration

### Included Minikube deployment

The default [`deploy/`](../deploy/) Kustomization includes
[`buildkit.yaml`](../deploy/buildkit.yaml), and
[`config.json`](../deploy/config.json) enables it. The manifest uses the tested
`moby/buildkit:v0.33.1` image, a single replica, the OverlayFS snapshotter, and an
ephemeral build cache. Cache is lost when its Pod is replaced; published images
remain in Dinki.

OverlayFS avoids copying the entire parent filesystem for each build step.
The cache filesystem must support OverlayFS mounts; an unsupported filesystem
causes worker startup to fail rather than silently selecting the much slower
native snapshotter. Changing snapshotters replaces the Pod and clears its cache.

Automatic worker garbage collection is enabled with a 10 GiB cache-use target,
a 2 GiB retention floor, and a 10 GiB free-space target on the backing filesystem.
BuildKit's default ordered policies clean old/reproducible cache first, then
other unused cache, including internal records when necessary. These are GC
targets, not hard quotas: active builds can exceed them, and GC cannot reclaim
actively used records. The free-space target observes the node filesystem, not
the devcontainer's entire Docker storage.

Kubernetes does not impose a cache quota in this manifest: the disk-backed
`emptyDir` has no `sizeLimit` and no pod ephemeral-storage limit is configured.
Adding those limits would cause pod eviction when exceeded, not graceful cache
pruning. Keep using host/Minikube cleanup separately when needed. Changes to
`buildkitd.toml` require a BuildKit pod restart to take effect; with the current
ephemeral volume that also discards the existing cache.

Create the additional server Secret using the existing Dink CA:

```sh
make buildkit-server
```

`make certificates` also creates `buildkit-tls` if missing, so the normal
Minikube bootstrap/Tilt setup provisions it. When ready to deploy the updated
Dink and Dinki images, resume the existing dev loop:

```sh
make dev
```

Alternatively, after loading updated images, apply the manifests with
`kubectl apply -k deploy`. This documentation does not automatically apply them.

To reclaim unused cache from the shared BuildKit worker:

```sh
make clean-buildkit
```

This administrative command prunes unused cache for **all tenants**, including
internal/frontend cache records. It leaves actively used records and published
Dinki images intact, but subsequent builds may need to download or rebuild
pruned content. Unlike `make clean-images`, it targets the BuildKit worker, not
the host Docker daemon or Minikube's containerd image store.

Certificate wiring:

- Dink reuses its existing `dink-tls` certificate as the BuildKit mTLS client.
- BuildKit mounts `buildkit-tls` for its server key pair and client CA. Its
  local authenticated probes use that certificate too: the existing
  `dinkle server issue` command issues both server and client EKUs.
- BuildKit mounts **only `ca.crt`** from `dinki-tls` to verify registry pushes.
  It never receives Dinki's private key or a static registry password.
- Probes connect to loopback with a TLS server-name override, so readiness
  does not depend on the Service already having a ready endpoint.

The BuildKit container is privileged for straightforward Minikube testing.
It has no Kubernetes API token and exposes only a ClusterIP Service. This is
not a production hardening profile. To disable the included backend, remove
`buildkit.yaml` from the Kustomization and clear `buildKit.url` in Dink's config
(also remove its Tilt resource entry if using Tilt).

Merge this example into Dink's configuration, adjusting names and mount paths:

```json
{
  "buildKit": {
    "url": "tcp://buildkitd.dink-system.svc.cluster.local:1234",
    "caFile": "/etc/dink/buildkit/ca.pem",
    "certFile": "/etc/dink/buildkit/client.pem",
    "keyFile": "/etc/dink/buildkit/client-key.pem",
    "serverName": "buildkitd.dink-system.svc.cluster.local",
    "registryURL": "https://dinki.dink-system.svc.cluster.local:5000"
  }
}
```

| Setting | Meaning |
| --- | --- |
| `url` | BuildKit gRPC address, `tcp://host:port` or `unix:///path/to/socket`. |
| `caFile` | CA bundle used by Dink to verify the BuildKit server. |
| `certFile`, `keyFile` | Dink's client identity for BuildKit mTLS; set both together. |
| `serverName` | Optional TLS server-name override. Otherwise the endpoint host is used. |
| `registryURL` | Dinki OCI origin reachable **from BuildKit**; defaults to Dink's `registry.url`. It is not `registry.pullHost`, the node-facing alias. |

The corresponding CLI flags are `--buildKitURL`, `--buildKitCAFile`,
`--buildKitCertFile`, `--buildKitKeyFile`, `--buildKitServerName`, and
`--buildKitRegistryURL`. Environment variables are `DINK_BUILDKIT_URL`,
`DINK_BUILDKIT_CA_FILE`, `DINK_BUILDKIT_CERT_FILE`, `DINK_BUILDKIT_KEY_FILE`,
`DINK_BUILDKIT_SERVER_NAME`, and `DINK_BUILDKIT_REGISTRY_URL`.

Setting any backend TLS option enables TLS; without TLS options, TCP uses
plaintext. Use plaintext only on a protected development network. Unix sockets
do not accept TLS options.

The operator is responsible for:

- Deploying BuildKit with a suitable worker, resources, storage, and security
  settings. Rootless BuildKit is an option where the cluster supports it.
- Mounting the backend CA and client key pair into Dink.
- Configuring BuildKit's server certificate and client CA, for example with
  `--tlscert`, `--tlskey`, and `--tlscacert`.
- Configuring **BuildKit's trust of Dinki**, independently of Dink's trust of
  BuildKit. For a private Dinki CA, mount it into the BuildKit Pod and configure
  `buildkitd.toml`:

  ```toml
  [registry."dinki.dink-system.svc.cluster.local:5000"]
    ca = ["/etc/buildkit/dinki/ca.pem"]
  ```

- Allowing Dink to reach BuildKit, and BuildKit to reach Dinki and upstream
  registries. Only Dink should access the backend control endpoint.

No static Dinki push password needs to be mounted into BuildKit.

## Export and session behavior

Dink handles native gRPC plus the legacy HTTP/1.1 `/grpc` and `/session` upgrades.
After the `/grpc` handshake, a standard `net/http` server serves HTTP/2 on the
hijacked connection; the upgrade does not use deprecated `x/net/http2` server APIs.
The session bridge forwards context files, upstream registry authentication,
secrets, SSH, and client-directed output services. Native gRPC uses Dink's
identity and authorization middleware; upgraded connections retain the identity
of their initiating HTTP request.

For a Docker-driver `moby` exporter, Dink:

1. Asks Dinki to map image tags using the same tenant naming as pull and tag
   operations. For tenant `team`, `app:test` maps to `team/app:test`, and
   `ghcr.io/org/app:test` maps to `team/ghcr.io/org/app:test`.
2. Issues an independent, short-lived build credential restricted to those
   exact repositories.
3. Translates the exporter into `image` with `push=true` and `store=false`.
4. Supplies that credential to BuildKit through the bridged authentication
   service, preserving the client's authentication for other registries.
5. Waits for the export/push to finish before reporting success, then revokes
   the credential on success, failure, or cancellation.

When `--push` is requested, Dink adds a second `image` exporter using the
original upstream tags and the client's session authentication. Both exports
must finish successfully. Internal registry credentials and TLS options are
kept separate from the upstream exporter.

Credentials expire after one hour; solves are limited to 59 minutes. Expired
credential records are reclaimed when another build credential is issued.
Node pull credentials remain read-only. Build credentials cannot delete content
or mount blobs from repositories outside their scope.

Images are stored in Dinki, not in the BuildKit worker's local image store.
Image IDs and `--iidfile` use Dinki's manifest/index digest. Returned
`image.name` metadata retains the caller's tags; progress can show the internal
push destination.

Explicit non-`moby` outputs are forwarded rather than redirected into Dinki.
In particular, `--output type=local,dest=...` sends files back to the client and
does not implicitly publish an image.

### Upstream publication with `--push`

```sh
docker login ghcr.io
docker --context dink buildx build --push -t ghcr.io/team/app:test .
docker --context dink image inspect ghcr.io/team/app:test
```

Tagged, single-platform Docker-driver builds support `--push`, including
multiple tags/repositories. A successful command publishes to the named upstream
registries and keeps the tenant-mapped image in Dinki. Unqualified tags use
Docker's normal Docker Hub naming; choose a repository you can push to.
`push-by-digest` and targeting Dinki's internal endpoint as an upstream remain
unsupported.

Publication is not atomic across registries/tags. If one destination fails,
the command fails, but images already published elsewhere are not rolled back.
A failed export may cancel the other export before it finishes.

Buildx's Docker driver currently performs an additional HTTP `docker push`
compatibility pass after the gRPC solve, because Dink does not advertise the
containerd image-store capability (which would also enable unrelated features).
The existing push endpoint handles this pass, and distribution inspection now
queries upstream metadata with the supplied registry authentication. Already
uploaded blobs can be reused, but extra requests/tag writes are expected.

For private upstream CAs, provision trust in BuildKit, Dinki (HTTP push), and
Dink (distribution inspection). BuildKit's `registry.insecure` exporter option
does not disable TLS verification for those HTTP compatibility calls. They
follow the existing registry policy: HTTPS except for loopback registries.

## Build history

The gateway supports `ListenBuildHistory` for listing records and watching build
events, plus `UpdateBuildHistory` for deleting, pinning/unpinning, and finalizing
a specific record. History is scoped to the authenticated namespace **and client
certificate common name**, just like build references; another client in the
same tenant does not see or modify these records.

New backend build references include an identity-hashed ownership prefix and an
encoded original reference. The gateway restores client-facing references,
image tags, and image-ID metadata when returning history. Ownership survives
Dink restarts without a separate index; BuildKit still loses its history when
its ephemeral state volume is replaced. Older hash-only build records cannot
be attributed for listing and are not exposed by this history implementation.

Backend status/repository/time filters are forwarded with an ownership predicate
on every OR branch before the backend applies its listing limit. Active and live
events are also checked individually for ownership. Reference filter expressions
are explicitly unsupported because backend references are encoded; select a
specific build reference instead.

Read-only content `Info` and `Read` calls are authorized against descriptors
referenced by the caller's own history records, including logs, traces, results,
provenance, and external errors. Arbitrary backend blobs cannot be read, and
backend GC labels are not exposed. This also preserves provenance collection
when a build uses `--metadata-file`. Ownership is checked against current backend
history on each request rather than cached permissions.

This is not full support for every Buildx history subcommand. General content
listing, writes, metadata updates, and deletion remain unsupported, as do
history archive imports and arbitrary traversal of blobs not directly referenced
by an owned history record.

After rebuilding Dink, create a new build and query its history:

```sh
docker --context dink buildx history ls
docker --context dink buildx history inspect '^0'
docker --context dink buildx history logs '^0'
```

## First-pass support and limitations

With Dink as the Docker endpoint and the default Docker-driver builder:

```sh
DOCKER_BUILDKIT=1 docker --context dink build -t app:test .
docker --context dink buildx build --builder default --load -t app:test .
docker --context dink image inspect app:test
```

Multiple tags in one image export are supported. One solve at a time is allowed
per session. This is not a complete Docker daemon or an unrestricted remote
BuildKit API:

- An image tag is required; untagged daemon-local exports are rejected.
- Docker-driver `--push` publishes upstream as well as into Dinki; publication
  is not atomic. `push-by-digest` remains unsupported.
- Docker's older HTTP `POST /build` and `/build/cancel` endpoints return an
  explicit unsupported error. Modern Docker builds require the Buildx plugin.
- `FROM app:test` does **not** resolve a previously built Dinki image
  automatically. Local-image resolution and Dinki base-image pull credentials
  are not implemented by this gateway.
- Raw LLB, frontend input definitions, source policies, privileged entitlements,
  and proxy-network requests are rejected.
- Multi-platform Docker-driver output, containerd image-store capabilities,
  cache inspection, history archive imports, and shared backend cache pruning are not advertised
  or implemented. HTTP cache pruning is unsupported when builds are enabled.
- Use one Dink replica and one BuildKit replica for this initial integration.
  Sessions and solves need the same gateway and backend; replica affinity and
  cross-replica session routing are not implemented.

Tenant registry permissions and build references are scoped, but a shared
BuildKit worker/cache is not a complete isolation boundary for mutually
untrusted tenants. Treat the backend as trusted infrastructure; do not expose
this first-pass integration as an unrestricted multi-tenant build service.

## Validation

The focused regression suite exercises native and upgraded sessions, protobuf
stream forwarding, upstream auth, tenant publication, credential expiry and
revocation, cancellation, and backend mTLS:

```sh
go test -race ./core/buildkit ./core/config ./core/registry/pullauth ./core/registry/api
```

There are also opt-in tests using the real Docker CLI and an external BuildKit
daemon. They create an in-memory Dinki and gateway and check image publication,
configuration/layers, image IDs, metadata, history, and explicit local output.
The push test additionally uses an authenticated upstream TLS registry to verify
dual publication and credential revocation after an authentication failure:

```sh
DINK_BUILDKIT_TEST_ADDR=tcp://<test-buildkit-host>:1234 \
DINK_BUILDKIT_TEST_REGISTRY_HOST=<this-test-process-host-reachable-from-buildkit> \
go test ./core/buildkit -run '^TestExternalBuildKitDocker(Build|Push)$' -count=1 -v
```

The test does not launch containers, deploy Pods, or restart Tilt. Supply an
isolated backend yourself. The test registry uses HTTP and ephemeral ports;
run this only on a trusted development network. Without the environment
variables, the live test is skipped.

By default the push test enables insecure registry transport for its ephemeral
upstream certificate. To test ordinary `--push` with certificate verification,
set `DINK_BUILDKIT_TEST_CA_DIR` to a dedicated empty directory mounted into the
isolated backend's system certificate directory. The test creates and removes
`upstream-ca.pem` there; use a fresh backend process for each run because Go caches
system roots. This is test-only setup, not production registry trust configuration.
