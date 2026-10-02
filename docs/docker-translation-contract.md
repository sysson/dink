# Docker Translation Contract

Dink translates supported Docker Engine API operations into Kubernetes and
registry operations. This document describes the current mappings and their
important constraints; it does not claim complete Docker compatibility. For
route-by-route support and known gaps, see the
[Docker API endpoint status tracker](docker-api-conformance.md).

## Request Boundary

- Router handlers own HTTP parsing, Docker API-version behavior, and response
  serialization. Kubernetes and registry behavior belongs in
  `core/translator`.
- Use Moby request and response types where they express the Docker contract.
  Do not copy daemon-only behavior that assumes a local runtime or image store.
- Derive tenant scope only from the authenticated request identity. Never use a
  caller-supplied namespace as authority.
- Resolve Docker names and IDs, including unique ID prefixes where expected;
  reject ambiguous matches.
- Preserve Docker response shapes and error meaning where practical. Reject
  unsupported settings or return a warning when best-effort translation is
  appropriate; do not silently discard requested behavior.
- Pass request contexts through Kubernetes, registry, and streaming calls.
  Streaming handlers must preserve Docker framing and stop on cancellation or
  source completion.

Reference implementations: [Moby router backend interfaces](https://github.com/moby/moby/tree/v2.0.0-beta.24/daemon/server/router)
and [Portainer D2K](https://github.com/portainer/d2k/tree/develop/internal/adapter).

## Containers

An ordinary Docker container is represented by a Deployment, initially scaled
to zero, with Kubernetes Services for DNS and any published ports. Starting and
stopping it changes the Deployment's replica count. An `AutoRemove` (`--rm`)
container is instead created as a suspended Job, runs once when started, and is
removed by Kubernetes shortly after completion. Its anonymous volumes follow
the Job lifecycle.

Dink resolves image references through the tenant registry and merges image
configuration defaults with the Docker create request. Supported environment,
command, working-directory, label, port, health-check, resource, and mount
settings are translated to Pod fields. Options without a useful Kubernetes
equivalent are rejected or reported as warnings; see the endpoint tracker for
current option-level details.

### Resource Defaults

The `kubernetes.defaultResources` setting supplies baseline CPU and memory
limits and requests. The shipped defaults are:

| Resource | Default |
| --- | --- |
| CPU limit | `500m` |
| Memory limit | `512Mi` |
| CPU request | `100m` |
| Memory request | `128Mi` |

A ConfigMap named `dink-resource-defaults` in a tenant namespace can override
individual values in its `resources.json` key:

```json
{"limits":{"cpu":"750m","memory":"1Gi"},"requests":{"cpu":"200m","memory":"256Mi"}}
```

Explicit Docker CPU or memory values take precedence, then tenant overrides,
then server defaults. Docker memory reservation becomes the memory request. A
default request is capped at its effective limit. Dink stores effective values
on the workload; inspect reports the running Pod's values if admission changed
them, otherwise it reports the workload template. The effective CPU request is
also exposed in inspect as
`HostConfig.Annotations["dink.io/requests.cpu"]`. Changes to defaults affect
new workloads only. Kubernetes LimitRange and ResourceQuota still enforce
cluster policy. Docker's zero-valued resource fields cannot distinguish an
omitted value from an explicit unlimited request, so both receive defaults.

### Service Discovery and Aliases

Every container gets a Service named after it. Docker network `Aliases` and
`DNSNames` create additional Services targeting the same Pods. These names are
tenant-wide because Kubernetes Service names are unique within a namespace,
whereas Docker's embedded resolver scopes names to each container's networks.

Aliases must be valid Kubernetes Service names and can be claimed only once per
tenant. A duplicate alias, or a container name that collides with an alias,
returns a conflict. An alias can therefore resolve across Docker networks even
when NetworkPolicy blocks the resulting connection. Alias Services are owned
by the workload and are removed with it; disconnecting a network removes only
that network's aliases. Swarm service aliases follow the same tenant-wide
scope.

## Networks and Isolation

Dink represents Docker networks as tenant-scoped `DockerNetwork` resources and
records membership on workload Pod labels. It creates an ingress-only
NetworkPolicy baseline for Dink Pods and one allow policy per logical network:

| Policy | Selects | Allows |
| --- | --- | --- |
| `dink-default-deny` | Dink-managed Pods in the tenant namespace | No ingress by itself |
| `dink-network-<object>` | Pods attached to that network | Ingress from Pods attached to the same network |
| `dink-published-<workload>` | A workload's Pods | Any source on its published container ports |

Policies are additive. The baseline is what makes per-network allow policies
isolating; a namespace-wide allow would defeat it. Per-network rules select
peers in the same namespace. The published-port rule is an exception: it allows
any source on the configured ports, including sources in other namespaces.
`connect` and `disconnect` update membership labels, which the existing
policies select without needing policy changes. Deleting a network removes its
owned policy.

These policies control ingress only. Pod egress, including DNS and access to
other network destinations, remains open; Dink does not reproduce Docker's
egress isolation. `--network none` has no per-network allow policy, though a
published-port policy can still expose configured ports. `--network host` uses
the node network and bypasses NetworkPolicy. Published-port policies allow any
source because NodePort and LoadBalancer traffic is commonly translated to a
node address. The policies are enforced only if the cluster CNI implements
Kubernetes NetworkPolicy; Dink cannot determine that from the Kubernetes API.

Bridge and overlay networks use the same membership and policy model. Overlay
networks report Swarm scope, but are logical Kubernetes-backed networks, not
Docker overlay interfaces. The built-in `ingress` network is also logical.

## Tenant Node Placement

The `kubernetes.nodePlacement` setting can dedicate nodes to tenant workloads.
It is disabled by default. For example:

```json
{"enabled":true,"labelKey":"dink.io/tenant","tolerate":true}
```

When enabled, each tenant Pod requires a node where `labelKey` is absent or
equals the tenant namespace. With `tolerate` enabled (the default), Dink also
adds a toleration for `labelKey=<namespace>:NoSchedule`. Affinity allows a
tenant onto its assigned nodes; the matching taint keeps unrelated workloads
off them. Affinity alone does not reserve nodes from non-Dink workloads.

Changing `labelKey` after labeling nodes strands the existing labels. Swarm
service placement constraints are combined with this affinity and cannot
override tenant placement.

## Swarm Resources

Dink presents Kubernetes as a Swarm. Cluster inspection and nodes are
cluster-wide; services, tasks, secrets, and configs are scoped to the
authenticated tenant.

| Docker Swarm resource | Kubernetes resource | Behavior |
| --- | --- | --- |
| Cluster identity | `kube-system` Namespace | Supplies the cluster ID and creation time; join tokens are empty. |
| Node | Node | Control-plane nodes report as managers; cordoned nodes report as `drain`. Requires cluster node-read permission. |
| Service | Deployment | The original Docker spec is stored in `dink.io/swarm-spec`; replicated mode is supported. |
| Task | Pod | Task IDs change when a Pod is replaced; slots follow creation order. |
| Secret | Secret | Payload is stored under `payload`; inspect does not return it. Separate from `se://` environment secrets. |
| Config | ConfigMap | Payload is stored under `payload` and returned by inspect. |

Docker object versions map to Kubernetes `resourceVersion` for optimistic
concurrency. Swarm service Deployments are excluded from standalone container
listing, inspect, and counts. Dink does not implement Swarm cluster membership
or node administration; use Kubernetes tooling for those operations. Global
and job service modes are rejected. Unsupported service settings are reported
as warnings where possible.

### Swarm Networks and Ports

The `bridge` and `overlay` drivers are accepted. A built-in `ingress` network
exists for stack deployment. Services join the networks named in
`TaskTemplate.Networks`, plus `ingress` when they publish a port, or `bridge`
when they request neither. Network membership uses the same NetworkPolicy
model described above, including for overlay networks.

| Publish mode | Kubernetes mapping |
| --- | --- |
| `ingress` | A LoadBalancer Service named `<service>-published`; external reachability depends on the cluster's load-balancer implementation. |
| `host` | `hostPort` on the task Pod. If no published port is supplied, Dink uses the target port and returns a warning. |

Each service also gets a ClusterIP Service for its VIP and service-name DNS.
Inspect reports live Service addresses and ports, including allocated NodePorts
where present, rather than always echoing the requested values. Updates retain
allocated NodePorts.

## Images, Volumes, and Plugins

Image operations use the tenant-visible `dinki` registry, not a node-local
Docker image store. Pull, push, list, inspect, history, tag, prune, and attestations
are registry-backed, with the limitations in the endpoint tracker. In
particular, multi-platform operations can use only manifests stored in the
registry. Tagged builds can use the optional external backend described below;
image load and export are not implemented.

`docker image ls --tree` lists all platform manifests advertised by each stored
image index, including platforms that have not been pulled. The Docker CLI dims
unavailable platforms. Availability means the manifest, config, and layers are
present in dinki, not cached or unpacked on a Kubernetes node. Content sizes
include only stored content; unavailable platform manifests have zero content
size. Dink has no unpacked image store, so unpacked sizes remain zero.

Retagging preserves the original manifest or index bytes and image ID, including
unpulled platform descriptors. Available content is copied into the destination
tenant repository; missing platforms remain unavailable.

`docker search` queries Docker Hub by default. Registry-qualified terms use that
registry's Docker-compatible `/v1/search` endpoint, not the OCI catalog or the
tenant image list. Stars and official-image filters are supported; the deprecated
automated-image filter returns no results when true. Registries without search
support return an error. Credentials supplied by the client support Basic,
bearer, and identity-token authentication. Search does not forward credentials
across origins on redirects.

`docker push` copies images from the tenant registry to the registry named in
the image reference. Tag an image with the destination name first when publishing
to a different repository. A named tag pushes that tag; `--all-tags` pushes all
tags of that tenant repository, excluding synthetic digest tags. Only layers
produce per-blob push progress rows; configs and manifests are uploaded without
separate rows. Progress and
failures use Docker's JSON stream format. A complete multi-platform index is
pushed unchanged. If content is missing, Dink follows Moby's single-platform
fallback and emits a `manifestPushedInsteadOfIndex` auxiliary notification.
When multiple stored platforms are ambiguous, specify `--platform` (API 1.46+).
An explicit platform push excludes the index and attestations. Missing platforms
are never automatically pulled during push.

Search and push use the request's `X-Registry-Auth` credentials and `X-Meta-*`
headers. As with Dink's pull endpoint, malformed auth headers are rejected.
External registries use HTTPS, except loopback registries which use HTTP.

The omitted or `local` volume driver creates a PVC using the cluster's default
StorageClass, with a default request of `1Gi` and access mode `ReadWriteOnce`.
The local driver's `size` option changes the requested capacity. A non-local
driver must have a registered Dink volume plugin. The plugin supplies PVC
parameters such as StorageClass, size, access modes, and annotations; Dink
validates those values and creates the claim. Removing a plugin-backed volume
also calls its driver. PVC provisioning and final reclamation depend on the
cluster's CSI and StorageClass policies. Standalone container host-path binds
and tmpfs mounts are unsupported; Swarm services have their own mount mapping.

Dink plugins are Kubernetes `Plugin` resources, not Docker-managed plugin
containers. Dink supports auth, secrets, and volume plugin types. Registrations
in `dink-system` are cluster-wide; registrations in a tenant namespace apply to
that tenant. The Docker plugin list and inspect endpoints expose a limited,
read-only projection of these registrations. Docker plugin install, enable,
disable, and other lifecycle operations are not implemented.

Environment values using `se://<provider>/<key>` are resolved through the
named provider. The built-in Kubernetes provider refers to a Secret key; other
providers use a Dink secrets plugin. Resolved plugin values are stored in an
owned Kubernetes Secret rather than in the workload's Docker configuration.

## Image Builds

An optional external BuildKit gateway supports tagged, single-platform builds
through the Docker driver's gRPC and session protocols. Execution can run in an
operator-managed Kubernetes Pod, while results are pushed into Dinki using
short-lived, repository-scoped credentials. Existing node pull credentials stay
read-only. Explicit client-directed outputs are not redirected into the registry.

Docker-driver `--push` adds an upstream image export using client session
credentials while retaining the tenant-mapped Dinki image. Both exports must
succeed; partial publications are not rolled back. Buildx may additionally use
the existing HTTP push endpoint and upstream distribution inspection as a
Docker-driver compatibility pass.

Build history listing, event streaming, and record updates are scoped to the
authenticated namespace and client common name. New references encode ownership
so history remains attributable after Dink restarts without an in-memory index.
Read-only content access is limited to descriptors referenced by owned history
records. General content mutation, history archive imports, and pre-integration
hash-only history are not supported.

Local `FROM` resolution, untagged exports, push-by-digest, legacy HTTP
builds, and shared-cache administration are not implemented. Backend TLS,
registry trust, replica constraints, and validation instructions are documented
in [External BuildKit builds](buildkit.md).

## System Information and Events

System responses report Dink and Kubernetes-backed data, not host Docker
daemon state. `/info` reports tenant-scoped workloads and image references,
while its Swarm section describes cluster-wide state. `/system/df` reports
registry image sizes and PVC provisioned capacity; it cannot report writable
container-layer bytes or actual volume usage. Container stats use Pod CPU and
memory metrics when Metrics Server samples are available; other Docker runtime
counters are not available. Events cover supported Dink workload and PVC
lifecycle changes, not the full Docker daemon event feed.

For exact route support, API-version gates, warnings, and remaining gaps, use
the [endpoint status tracker](docker-api-conformance.md). That tracker is a
source audit, not external conformance certification.