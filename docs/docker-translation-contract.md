# Docker Translation Contract (Draft)

This document defines the boundary between dink's Docker-compatible HTTP API
and its Kubernetes-backed translation layer. It is a starting contract, not a
claim that every registered endpoint is implemented.

For the current route-by-route implementation status and known endpoint
caveats, see the [Docker API endpoint status tracker](docker-api-conformance.md).

## Responsibility Boundary

- Keep the Docker Engine route surface and API-version behavior aligned with
  Moby. Route handlers parse Docker requests, call a translator, and serialize
  Docker responses; Kubernetes mapping belongs in `core/translator`.
- Use Moby's router backend interfaces and Docker API types as the reference
  for translator method signatures. Do not copy daemon-only behavior that
  assumes a local container runtime or image store.
- Aim to support as much of the Docker API as practical. Prefer translating
  real Kubernetes resources where semantics fit and emulating Docker-facing
  behavior where they do not, as with logical networks. Reserve unsupported
  errors for operations that cannot be represented safely or usefully.
- Preserve Docker response shapes and error meaning at the HTTP boundary, but
  document unavoidable semantic differences.

Reference implementations: [Moby router backend interfaces](https://github.com/moby/moby/tree/v2.0.0-beta.24/daemon/server/router)
and [Portainer D2K](https://github.com/portainer/d2k/tree/develop/internal/adapter).

## Cross-Cutting Rules

- Tenant-scoped operations derive their namespace from the authenticated
  identity in the request context. They must not accept a namespace supplied
  by the caller as authority.
- Resource lookup accepts Docker names and IDs, including unique ID prefixes
  where Docker clients expect them. Ambiguous prefixes fail; IDs must remain
  resolvable across the resource lifecycle.
- Pass `context.Context` through Kubernetes calls and streaming operations so
  request cancellation and deadlines are honored.
- Map Kubernetes not-found, conflict, forbidden, and invalid-input errors to
  the corresponding Docker API error behavior.
- Do not silently discard a requested Docker setting. Reject it or return a
  documented warning when the API contract permits a best-effort translation.
- Streaming endpoints must preserve Docker framing and terminate on request
  cancellation or source completion.

## Container Resource Defaults

The `kubernetes.defaultResources` server setting provides baseline CPU and memory
limits and requests. The shipped defaults are 500m/512Mi for limits and
100m/128Mi for requests. An optional ConfigMap named `dink-resource-defaults`
in the authenticated tenant's namespace overrides individual fields. Only
administrators should be able to edit this policy ConfigMap. Its
`resources.json` key contains the same `limits`/`requests` shape, for example:

```json
{"limits":{"cpu":"750m","memory":"1Gi"},"requests":{"cpu":"200m","memory":"256Mi"}}
```

Explicit Docker CPU or memory limits take precedence; any omitted value falls
back to the tenant override, then the server default. Docker memory reservation
becomes the memory request; the effective CPU request is exposed in inspect as
`HostConfig.Annotations["dink.io/requests.cpu"]`. A default request above an
explicit container limit is capped at that limit. Dink persists effective
resource values on the Deployment before creating it. Inspect reports the
current running Pod's CPU and memory resources if admission changed them, and
the Deployment template otherwise. Changes to either default policy apply only
to new containers. Kubernetes LimitRange and ResourceQuota remain enforcement
backstops, not the source of inspect values. Docker's zero-valued resource
fields cannot distinguish omitted from an explicit request for unlimited
resources; both receive defaults.

## Surface Contract

| Surface | Kubernetes-side contract | Current implementation status |
| --- | --- | --- |
| Containers | Represent a standalone container as a workload, initially a Deployment. Merge image defaults with request overrides before translating supported settings; derive inspect/list/state from Deployments and Pods. Use Pod logs/exec APIs for runtime interaction. | Partial: create resolves a registry digest and merges image Env, Entrypoint/Cmd, WorkingDir, ExposedPorts, labels, and other Docker config defaults with request values. It persists merged config, creates a Deployment and pull Secret, plus a headless/ClusterIP DNS Service. Explicit port bindings create a separate LoadBalancer Service; publish-all creates a NodePort Service. Create defaults to the `bridge` network label, honors requested endpoint networks, maps host mode to `hostNetwork`, and maps named, anonymous, and image-declared volumes to PVCs. Docker memory limits, NanoCPUs or CPU quota/period become Pod limits when the CPU value is representable in whole millicores; memory reservation becomes a Pod request. Other Docker resource options are rejected rather than ignored. Start/stop/kill/restart/remove, inspect, list, prune, wait, and Pod logs are implemented with documented limitations. Removing a container whose Deployment still has replicas is rejected with a conflict unless force is requested, matching Docker. Stats uses PodMetrics CPU and memory readings when metrics-server is available; Docker network, disk, PID, and historical CPU counters are not available from PodMetrics. Exec create/start/inspect/TTY-resize and live HTTP attach use Pod exec/attach and Docker hijacked stream framing; exec IDs are process-local and scoped to their original Pod. Exec honors the initial console size by seeding the Kubernetes terminal size queue and honors detach keys by ending the stream on the escape sequence. Detached exec, WebSocket attach, historical attach logs, host-path/tmpfs mounts, custom volume drivers, and custom exec users/privileges/env/working directories remain unsupported. List supports `id`, `name`, `status`, `label`, and `ancestor` filters; several merged config fields still have no Kubernetes Pod mapping. Copy methods remain unimplemented. |
| Networks | Treat Docker networks as tenant-scoped resources and target real Pod isolation with Kubernetes NetworkPolicies. Record membership on workloads and reconcile policies as membership changes. Where the cluster CNI does not enforce NetworkPolicies, report that limitation rather than claiming isolation. | Partial: list/create/inspect/delete/prune and `connect`/`disconnect` use `DockerNetwork` resources and workload membership labels. NetworkPolicy reconciliation is not implemented, so labels do not yet enforce isolation. |
| Volumes | Define named Docker volumes as PVC-backed resources. Specify supported drivers, size, access mode, storage class, and deletion/reclaim behavior before implementing create/remove. | Partial: omitted/`local` driver volumes create namespace-scoped PVCs using the default StorageClass, `ReadWriteOnce`, and a default `1Gi` request; `DriverOpts.size` overrides the size. List/inspect/create/remove/prune operate on Dink-owned PVCs. Container named volumes, legacy named `-v` binds, and image-declared anonymous volumes become PVC mounts. Unsupported drivers/options, host-path binds, tmpfs, cluster volumes, and the experimental update route fail explicitly. PVC provisioning still depends on the cluster's default StorageClass and CSI setup. |
| Images and registry | Keep registry operations distinct from node-local image state. Resolve tenant-visible image names through dinki; Kubernetes pulls the resulting image when a Pod is scheduled. Emulate Docker image metadata and operations from tenant-visible registry/workload data where useful. | Partial: image list/inspect/history/delete/attestations/pull delegate to the registry client; other image operations are stubs. Pull/push/auth use Moby's API types; image filters use dink's `filters.Args`. |
| Build | Treat building as a separate capability from deploying an image. Define the Builder's execution backend and how its output becomes available through the tenant registry; support Docker build options when that backend can honor them. | Build/cache-prune/cancel and build disk usage now have typed Moby-shaped contracts and explicit not-implemented errors. |
| System and events | Report dink/Kubernetes facts in Docker response types. Derive workload events from Kubernetes watches/events; do not imply that system-wide disk usage is a local Docker daemon measurement. | Version and registry auth are implemented. Info, disk usage, events, cluster info, build disk usage, and status now have typed contracts and explicit not-implemented errors. |
| Swarm | Keep Swarm compatibility separate from standalone container operations. Emulate cluster identity and map services, tasks, secrets, and configs to Kubernetes resources where practical. Joining or leaving a real Swarm is not meaningful and should fail clearly. | Typed Moby-shaped contracts exist; operations return `ErrNotImplemented`. |
| Plugins and checkpoints | Investigate whether registry metadata or workload resources can provide useful Docker-compatible emulation. Operations requiring a host Docker daemon or CRIU remain unsupported unless a concrete backend is introduced. | Stub. |

### Recently Enabled Operations

`POST /containers/{name}/update` accepts the CPU and memory resource fields already supported at container creation. Unsupported resource fields and restart-policy changes are rejected; updating a running Deployment replaces its Pod and returns a warning.

Container creation without a Docker name generates a friendly, DNS-valid adjective-noun name with a random numeric suffix. The Deployment, Pod labels, selectors, container, and related Services share that name.

`POST /images/{name}/tag` copies the image content available in the tenant registry into the target tenant repository. When a pulled multi-platform index contains platforms whose manifests were not stored, the new tag includes only the stored platform manifests.

The volume API supports the Docker `local` driver (or an omitted driver) through PVCs. It defaults to `1Gi` and `ReadWriteOnce`, uses the cluster default StorageClass, and accepts `--opt size=<quantity>`. Named mounts, legacy named `-v` mounts, and image-declared anonymous volumes are attached as PVCs. Host-path binds, tmpfs, arbitrary driver options, custom drivers, and `PUT /volumes/{name}` remain unsupported.

## Operation Inventory

The method groups below follow Moby's router backend interfaces at the version
referenced above. This is the signature and coverage inventory; a method's
presence does not imply that its Kubernetes behavior has been designed or
implemented. Prefer Moby's Docker API request/response types and streaming
contracts, adding dink-specific types only where needed.

| Router family | Moby operation groups | Dink translator coverage |
| --- | --- | --- |
| Container | State: create, kill, pause, rename, resize, restart, remove, start, stop, unpause, update, wait. Monitor: changes, inspect, logs, stats, top, list. Exec: create, inspect, resize, start, exists. Copy: archive, export, extract, stat. Also attach, prune, and commit. | Create/start/stop/kill/restart/remove/inspect/list/prune/wait are implemented against Deployments, Pods, and Services. Named, legacy named-bind, and image-declared anonymous mounts use PVCs and are reported by inspect. Logs use Pod log streams; stats use metrics.k8s.io Pod CPU and memory samples. Exec create/start/inspect/resize and live HTTP attach use Kubernetes Pod streaming. Remaining unsupported operations return explicit 501 errors. Dink adds request context for tenant scope/cancellation. Moby's raw sysinfo helper is omitted because dink parses Docker's create request directly. |
| Network | List/inspect, summaries, create, connect/disconnect, delete, prune; cluster network list/inspect/create/remove. | Tenant summaries/inspect/create/delete/prune and connect/disconnect membership updates are implemented. Cluster methods remain unsupported; filters use dink's tenant-aware `filters.Args`. |
| Image | Delete, history, list, get, inspect, attestations, tag, prune; load/import/export; pull/push; search. | Delete/history/list/inspect/attestations/pull delegate to dinki. Get delegates to dinki inspect; tag/prune/load/import/export/push/search have typed contracts and return `ErrNotImplemented`. Pull/push/auth use Moby's API types; filters use dink's `filters.Args`. |
| Volume | List/get/create/remove/prune; cluster get/list/create/remove/update and manager check. | Non-cluster list/get/create/remove/prune use PVCs for the default local driver. Cluster-volume methods remain unsupported. Request context and tenant-aware `filters.Args` are used instead of Moby's daemon-local functional options. |
| Build | Build, prune cache, cancel. | All methods have typed contracts and explicit `ErrNotImplemented` results. |
| System | Info, version, disk usage, subscribe/unsubscribe events, registry auth; cluster info; build disk usage; status. | Version and registry auth are implemented; remaining methods have typed contracts and explicit `ErrNotImplemented` results. |
| Swarm | Init/join/leave/inspect/update/unlock; services, nodes, tasks, secrets, configs and service logs. | All methods now have typed Moby-shaped contracts and explicit `ErrNotImplemented` results. |
| Plugin | Disable/enable/list/inspect/remove/set, privileges, pull/push/upgrade, create from context. | All methods have typed Moby-shaped contracts and explicit `ErrNotImplemented` results. |
| Checkpoint | Create/delete/list. | All methods have typed Moby-shaped contracts and explicit `ErrNotImplemented` results. |
| Distribution | Get repositories. | The typed Moby contract returns `ErrNotImplemented`. |
| Session and gRPC | HTTP session upgrade/stream handling; gRPC service registration. | Session handling returns `ErrNotImplemented`; gRPC registration accepts Moby's `*grpc.Server` but no service is registered yet. |

### Network Policy Direction

Docker network membership is recorded as workload-template labels at create
time (defaulting to `bridge`) and should also be updated by connect/disconnect.
Policies should select those labels to permit traffic between Pods sharing a logical network. A
baseline policy for dink-managed Pods is needed if separate Docker networks are
to be isolated from one another; per-network allow policies alone are additive
and do not create isolation. The design must also specify DNS/egress behavior,
how Pods attached to multiple networks are handled, and how built-in `host`,
`none`, and `bridge` networks map.

Kubernetes NetworkPolicy objects are only effective when the installed network
plugin enforces them. The API's presence is not proof of enforcement, so the
contract must expose the cluster prerequisite and avoid presenting a logical
network as a security boundary when enforcement is unavailable.

## Interface Shape

Translator interfaces should be organized by the existing router/resource
boundaries and grouped by capability where that improves readability. Replace
placeholder no-argument methods with typed methods matching the corresponding
Moby router backend contract, including Moby request/response types, streams,
and context parameters where applicable. Add dink-specific interfaces only for
Kubernetes or registry capabilities that Moby does not express.

The method contract should make unsupported behavior representable through an
error. Empty method bodies and zero-value responses are not valid substitutes
for an implemented operation.

## Next Work

The initial contract inventory is in place and typed interfaces compile against
the Moby-shaped request/response surface. Next implement container inspect/list
and then logs/exec, followed by PVC-backed volumes and enforced network
attachment policies.