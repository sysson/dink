# Docker API Endpoint Status

This is a route-by-route source audit of the HTTP routes mounted by dink. It is
intended as a working status tracker, not a claim that the Docker Engine API
conformance suite passes. `100%` means no known behavior gap was found in the
registered handler; it is not an external test certification. Recheck entries
as the implementation changes.

Routes are available both unversioned and under `/v{version}`. API-version
minimums are called out in the endpoint row. `/debug/*` and `/grpc` are included
because dink mounts them alongside the Docker API routes.

| Status | Meaning |
| --- | --- |
| 100% | Handler implements the registered endpoint behavior with no known caveat. |
| Partial | Main operation exists, but supported options, Docker semantics, or returned data differ. |
| Todo | Stub, empty response, or explicitly unsupported operation; not usable as the Docker endpoint. |

## Containers

| Method and endpoint | Status | Caveats |
| --- | --- | --- |
| `HEAD /containers/{name}/archive` | Todo | Explicitly returns 501; archive stat is not implemented. |
| `GET /containers/json` | Partial | Lists Kubernetes-backed containers. Docker host/runtime fields and size reporting are not equivalent to a local daemon. |
| `GET /containers/{name}/export` | Todo | Explicitly returns 501. |
| `GET /containers/{name}/changes` | Todo | Filesystem change inspection is not implemented. |
| `GET /containers/{name}/json` | Partial | Inspect is derived from Deployments/Pods; runtime and host-specific fields can differ. |
| `GET /containers/{name}/top` | Todo | Process listing is not implemented. |
| `GET /containers/{name}/logs` | Partial | Pod logs need metrics/runtime support as appropriate. Only stdout is emitted; stderr-only requests and Docker log `details` return 501. |
| `GET /containers/{name}/stats` | Partial | Reports Pod CPU/memory from `metrics.k8s.io/v1beta1`; streams fresh Docker samples every two seconds using the latest usage rate, even when the Metrics Server timestamp is unchanged. Without metrics-server or a Pod sample it returns zero usage. It does not provide Docker network, disk, PID, or historical CPU counters. |
| `GET /containers/{name}/attach/ws` | Todo | WebSocket attach is not implemented. |
| `GET /exec/{id}/json` | Partial | Exec records are process-local and scoped to the Pod that created them; they do not survive process restart. |
| `GET /containers/{name}/archive` | Todo | Archive download is not implemented. |
| `POST /containers/create` | Partial | Creates a Deployment, Services, and PVC mounts; `--rm` (AutoRemove) containers instead become a suspended Job that runs once and is removed by Kubernetes shortly after it finishes, taking its Services and anonymous volumes with it. Network aliases become additional Services, so they resolve tenant-wide rather than per network. Docker `HEALTHCHECK` CMD/CMD-SHELL maps to a Kubernetes readiness probe, not liveness; start timing may be approximated with warnings. Only supported Docker settings are translated; unsupported resource options are rejected. PVCs require cluster storage. See [translation contract](docker-translation-contract.md#container-resource-defaults). |
| `POST /containers/{name}/kill` | Partial | Maps to scaling the Deployment down; only empty/default, `KILL`/`SIGKILL`, and `INT`/`SIGINT` signals are accepted. |
| `POST /containers/{name}/pause` | Todo | Translator returns not implemented. |
| `POST /containers/{name}/unpause` | Todo | Translator returns not implemented. |
| `POST /containers/{name}/restart` | Partial | Restarts by changing Deployment replicas; Docker process-level timing and state semantics differ. `--rm` containers cannot be restarted. |
| `POST /containers/{name}/start` | Partial | Starts the Deployment. Docker checkpoint parameters are unsupported. |
| `POST /containers/{name}/stop` | Partial | Stops by scaling the Deployment down; signal and timeout behavior are constrained by the Kubernetes backend. |
| `POST /containers/{name}/wait` | Partial | Waits on Kubernetes workload state; exit status is synthesized, not a container-runtime exit status. |
| `POST /containers/{name}/resize` | Partial | Resizes active TTY attach sessions only; dimensions are limited by Kubernetes. |
| `POST /containers/{name}/attach` | Partial | Live HTTP hijacked attach is supported. Historical logs, detach keys, and non-stream attach are unsupported. |
| `POST /containers/{name}/exec` | Partial | Attached Pod exec is supported. Custom user, privileges, environment, and working directory options are not fully supported; exec IDs are process-local. |
| `POST /exec/{id}/start` | Partial | Attached streaming only; detached exec returns 501. TTY must match the create request. |
| `POST /exec/{id}/resize` | Partial | Resizes an active exec TTY; IDs are process-local and Pod-scoped. |
| `POST /containers/{name}/rename` | Todo | Translator returns not implemented. |
| `POST /containers/{name}/update` | Partial | CPU/memory resource updates only; restart-policy changes and other resource fields are rejected. Updating a running Deployment replaces its Pod and returns a warning. `--rm` containers cannot be updated. |
| `POST /containers/prune` | Partial | Removes eligible Dink-managed workloads; filter behavior is limited to implemented container filters. Minimum API version 1.25. |
| `POST /commit` | Todo | Container commit is not implemented. |
| `PUT /containers/{name}/archive` | Todo | Archive upload is not implemented. |
| `DELETE /containers/{name}` | Partial | Removes the Deployment and Services. `v=1` (and `--rm`) removes the container's anonymous volumes that no other container uses, as Docker does. Removing links is unsupported; a running container conflicts unless forced. |

## Images

| Method and endpoint | Status | Caveats |
| --- | --- | --- |
| `GET /images/json` | Partial | Lists tenant-visible registry images, not a node-local Docker image store. From API 1.47, `manifests=true` supports `docker image ls --tree`: all platforms in the stored index are listed, with availability and sizes based on content present in dinki. Missing platform manifests have zero content size; unpacked size is always zero. Parent size counts stored content once across platforms. `shared-size` and `identity` are parsed but not implemented; Dink does not enforce Docker's validation that `identity=1` requires `manifests=1`. |
| `GET /images/search` | Partial | Queries Docker Hub by default, or a registry-qualified Docker-compatible `/v1/search` service. Supports `limit` (default 25, maximum 100), `stars`, `is-official`, auth, and `X-Meta-*` headers. Deprecated `is-automated=true` returns no results. OCI registries without a search service return an explicit error. |
| `GET /images/get` | Todo | Image export is not implemented; handler currently returns an empty success. |
| `GET /images/{name}/get` | Todo | Image export is not implemented; handler currently returns an empty success. |
| `GET /images/{name}/history` | Partial | History comes from registry metadata; platform selection is supported from API 1.48. |
| `GET /images/{name}/json` | Partial | Inspect is registry-derived and does not represent a node-local image. Manifests/platform options are API-version-gated. |
| `GET /images/{name}/attestations` | Partial | Registry-backed; only one `platform` value is accepted. Minimum API version 1.55. |
| `POST /images/load` | Todo | Handler currently returns an empty success; image load is not implemented. |
| `POST /images/create` | Partial | Pull delegates to the tenant registry. Multi-platform pulls only retain manifests available to the registry; platform option starts at API 1.32. |
| `POST /images/{name}/push` | Partial | Pushes tenant-registry content to the named external registry, streaming Docker JSON progress. A tag selects one image; no tag pushes all repository tags. Supports registry auth and `X-Meta-*` headers. API 1.46 adds a single JSON-encoded `platform`; explicit platform pushes omit the index and attestations. Complete indexes are preserved; incomplete indexes fall back to an available platform with a Docker auxiliary notification. No content is fetched upstream to fill missing platforms. |
| `POST /images/{name}/tag` | Partial | Copies image content in the tenant registry. For a pulled multi-platform index, only stored platform manifests are included. |
| `POST /images/prune` | Partial | Prunes unused tagged tenant-registry images when `dangling=false`; default dangling-only prune returns no results because the registry does not store untagged images. Minimum API version 1.25. |
| `DELETE /images/{name}` | Partial | Deletes tenant registry content, not node-local image data; behavior depends on registry capabilities and references. |

## Networks

Dink reconciles Docker networks to Kubernetes NetworkPolicies: a default-deny
ingress policy per tenant namespace, one allow policy per network, and one
allow policy per workload that publishes ports. Whether those policies are
*enforced* depends on the cluster's CNI; Kubernetes accepts them either way.
Containers using `--network host` bypass NetworkPolicy entirely.

| Method and endpoint | Status | Caveats |
| --- | --- | --- |
| `GET /networks` | Partial | Lists tenant-scoped logical `DockerNetwork` resources. Overlay networks report `swarm` scope and the built-in `ingress` network is always present. |
| `GET /networks/` | Partial | Same list behavior as `GET /networks`. |
| `GET /networks/{id}` | Partial | Logical network inspection; not a host network-driver inspection. |
| `POST /networks/create` | Partial | Creates a logical resource with the `bridge` or `overlay` driver, plus the NetworkPolicy that isolates its members; `overlay` is swarm-scoped and may be `attachable`. Other drivers, `internal`, IPAM configuration, and creating a second `ingress` network are rejected. |
| `POST /networks/{id}/connect` | Partial | Updates workload membership labels and creates a Service per requested alias. Policies select on those labels, so isolation follows without reconnecting interfaces; it does not configure real network interfaces. Endpoint settings other than aliases are rejected. |
| `POST /networks/{id}/disconnect` | Partial | Updates workload membership labels and removes that network's alias Services. |
| `POST /networks/prune` | Partial | Prunes eligible tenant logical networks; no host network driver is involved. Minimum API version 1.25. |
| `DELETE /networks/{id}` | Partial | Deletes a tenant logical network; Kubernetes garbage-collects the owned NetworkPolicy. The built-in `ingress`, `bridge`, `host`, and `none` networks cannot be removed. |

## Volumes

| Method and endpoint | Status | Caveats |
| --- | --- | --- |
| `GET /volumes` | Partial | Lists Dink-owned PVC-backed volumes only; depends on cluster storage and the default StorageClass. |
| `GET /volumes/{name}` | Partial | Inspects a Dink-owned PVC; custom drivers and non-Dink volumes are unsupported. |
| `POST /volumes/create` | Partial | Omitted/`local` driver uses a PVC, default `1Gi` and `ReadWriteOnce`; `size` is supported. Non-local drivers require a registered Dink volume plugin, which supplies PVC parameters; unsupported drivers and options are rejected. |
| `POST /volumes/prune` | Partial | Prunes eligible Dink-owned PVCs. From API 1.42 only anonymous volumes are pruned unless `all=true`, as Docker does. Docker volume-driver semantics do not apply. Minimum API version 1.25. |
| `PUT /volumes/{name}` | Todo | Explicitly returns 501. Minimum API version 1.42. |
| `DELETE /volumes/{name}` | Partial | Removes a Dink-owned PVC; actual reclamation depends on the StorageClass reclaim policy. |

## Build and System

| Method and endpoint | Status | Caveats |
| --- | --- | --- |
| `POST /build` | Todo | Returns an explicit unsupported error. Tagged modern Docker/Buildx builds use the optional gRPC BuildKit gateway instead; see [BuildKit integration](buildkit.md). |
| `POST /build/prune` | Partial | Reports nothing pruned when builds are disabled. With an external backend, rejects pruning because its cache is shared. Minimum API version 1.31. |
| `POST /build/cancel` | Todo | Returns an explicit unsupported error; cancel the Buildx request to cancel its solve. |
| `OPTIONS /{anyroute:.*}` | Partial | Returns 200 for any matched path but does not implement Docker's complete OPTIONS response headers. |
| `GET /_ping` | 100% | Returns `OK` with no-cache headers. |
| `HEAD /_ping` | 100% | Returns the ping headers with an empty body. |
| `GET /events` | Partial | Streams namespace-scoped container lifecycle and Dink-managed PVC events, with retained Kubernetes-event history and Docker filters. It is not a complete Docker daemon event feed; only current resources can be associated with historical events. |
| `GET /info` | Partial | Reports Dink version and authenticated-namespace Deployment counts and distinct workload image references; it is not node-wide Docker daemon or registry inventory. The `Swarm` section reports cluster-wide state and advertises an active manager, which puts Docker clients and Portainer into Swarm mode. |
| `GET /version` | Partial | Uses the Dink version translator; values describe Dink rather than a local Docker Engine installation. |
| `GET /system/df` | Partial | Namespace-scoped. Image sizes come from the tenant registry and an image is active when a container references it. Container sizes are always 0 (writable layers live on nodes). Volume size is the PVC's provisioned capacity, not bytes used, and `RefCount` is 0 or 1. Build cache is always empty. API 1.42+ supports `type`; API 1.52+ supports typed usage fields and `verbose`, retaining legacy fields unless verbose output is requested. |
| `POST /auth` | Partial | Reads `AuthConfig` from the JSON body and authenticates against its `serveraddress` using the registry challenge flow. Credentials are not persisted; clients resend them in `X-Registry-Auth` on later requests. |

## Swarm

Dink presents the Kubernetes cluster as a Swarm: services are Deployments,
tasks are Pods, secrets are Secrets, and configs are ConfigMaps. Services,
tasks, secrets, and configs are scoped to the authenticated namespace; cluster
inspection and nodes are cluster-wide. Cluster membership endpoints return an
explanatory error because a Kubernetes cluster cannot join or leave a Swarm.

| Method and endpoint | Status | Caveats |
| --- | --- | --- |
| `POST /swarm/init` | Todo | Returns an error explaining that the cluster already exists and is managed with kubectl. |
| `POST /swarm/join` | Todo | Returns an error explaining that a Kubernetes cluster cannot join a Docker Swarm. |
| `POST /swarm/leave` | Todo | Returns an error explaining that membership is managed with kubectl and the cluster provider. |
| `GET /swarm` | Partial | Cluster-wide. Identity and creation time come from the `kube-system` namespace; join tokens are empty and Raft, CA, dispatcher, and orchestration settings are not represented. |
| `GET /swarm/unlockkey` | Todo | Returns an error explaining that Dink has no Raft store to autolock. |
| `POST /swarm/update` | Todo | Returns an error explaining that Swarm cluster settings have no Kubernetes equivalent. |
| `POST /swarm/unlock` | Todo | Returns an error explaining that Dink has no Raft store to autolock. |
| `GET /services` | Partial | Lists tenant Deployments labelled as Swarm services. Supports `id`, `name`, `label`, `mode`, and `runtime` filters and the API 1.41 `status` option. |
| `GET /services/{id}` | Partial | Spec is returned from the stored Docker spec; `insertDefaults` fills replicated mode, restart policy, and endpoint mode. `Endpoint.Ports` and `VirtualIPs` are read back from the live Kubernetes Services, so an auto-assigned NodePort and the ClusterIP are reported rather than the requested values. |
| `POST /services/create` | Partial | Creates a Deployment, a ClusterIP Service for the VIP, a LoadBalancer Service for ingress-published ports, and a Service per network alias. `mode=host` ports become Kubernetes `hostPort` bindings instead, and a host-mode port with no published port falls back to the target port with a warning. The service joins the Docker networks named in `TaskTemplate.Networks`, plus `ingress` when it publishes a port, and `bridge` when it asks for neither. Replicated mode only; global and job modes are rejected. Mounts support volumes and tmpfs; binds are rejected. Secrets and configs mount from Kubernetes Secrets/ConfigMaps. Placement constraints map to node affinity for `node.hostname`, `node.role`, `node.labels.*`, and `engine.labels.*`. User, privileges, init, isolation, ulimits, stop signal, and restart policy are reported as warnings. |
| `POST /services/{id}/update` | Partial | Rebuilds the Deployment from the new spec and uses `version` as the Kubernetes resourceVersion. Already-allocated node ports are retained so published ports do not move. Renaming and server-side rollback are rejected. |
| `DELETE /services/{id}` | Partial | Deletes the Deployment; owned Services are garbage-collected by Kubernetes. |
| `GET /services/{id}/logs` | Partial | Merges the Pod logs of the service's tasks. Only stdout is emitted; stderr-only requests and log `details` return 501. Interleaving across tasks is not ordered by timestamp. |
| `GET /nodes` | Partial | Cluster-wide list of Kubernetes nodes. Control-plane nodes report as managers; cordoned nodes report as `drain`. Supports `id`, `name`, `role`, `membership`, and `label` filters. Requires cluster `nodes` read permission. |
| `GET /nodes/{id}` | Partial | Matches on node name or Swarm ID prefix. |
| `DELETE /nodes/{id}` | Todo | Returns an error directing the caller to `kubectl delete node` and provider node lifecycle tooling. |
| `POST /nodes/{id}/update` | Todo | Returns an error directing the caller to `kubectl label`, `kubectl cordon`, and `kubectl drain`. |
| `GET /tasks` | Partial | Lists the Pods behind the namespace's Swarm services. Slots are assigned by creation order, not Swarm slot identity. `NetworksAttachments` reports the task's Docker networks and its Pod IP. Node IDs need cluster node read permission. Supports `id`, `name`, `label`, `node`, `service`, and `desired-state` filters. |
| `GET /tasks/{id}` | Partial | Matches on task ID prefix; task IDs change when a Pod is replaced. |
| `GET /tasks/{id}/logs` | Partial | Same Pod log limitations as service logs. |
| `GET /secrets` | Partial | Lists tenant Kubernetes Secrets labelled as Swarm secrets. Minimum API version 1.25. |
| `POST /secrets/create` | Partial | Creates a Kubernetes Secret. External secret drivers and templating are rejected. Minimum API version 1.25. |
| `DELETE /secrets/{id}` | Partial | Deletes the Kubernetes Secret. Minimum API version 1.25. |
| `GET /secrets/{id}` | Partial | Never returns the payload, as Docker does. Minimum API version 1.25. |
| `POST /secrets/{id}/update` | Partial | Labels only, as Docker does; data and name changes are rejected. Minimum API version 1.25. |
| `GET /configs` | Partial | Lists tenant ConfigMaps labelled as Swarm configs. Minimum API version 1.30. |
| `POST /configs/create` | Partial | Creates a ConfigMap. Templating is rejected. Minimum API version 1.30. |
| `DELETE /configs/{id}` | Partial | Deletes the ConfigMap. Minimum API version 1.30. |
| `GET /configs/{id}` | Partial | Returns the payload, as Docker does for non-secret configs. Minimum API version 1.30. |
| `POST /configs/{id}/update` | Partial | Labels only; data and name changes are rejected. Minimum API version 1.30. |

## Plugins, Checkpoints, Distribution, Session, and gRPC

| Method and endpoint | Status | Caveats |
| --- | --- | --- |
| `GET /plugins` | Partial | Lists the tenant-visible Dink plugin registrations, including cluster-scoped plugins, as a limited Docker plugin projection with readiness and capabilities. It is not a node-local Docker plugin inventory. |
| `GET /plugins/{name}/json` | Partial | Inspects a tenant-visible Dink plugin registration as a limited Docker plugin projection; Docker plugin configuration and runtime details are not exposed. |
| `GET /plugins/privileges` | Todo | Handler currently returns an empty success; plugins are not implemented. |
| `DELETE /plugins/{name}` | Todo | Handler currently returns an empty success; plugins are not implemented. |
| `POST /plugins/{name}/enable` | Todo | Handler currently returns an empty success; plugins are not implemented. |
| `POST /plugins/{name}/disable` | Todo | Handler currently returns an empty success; plugins are not implemented. |
| `POST /plugins/pull` | Todo | Handler currently returns an empty success; plugins are not implemented. |
| `POST /plugins/{name}/push` | Todo | Handler currently returns an empty success; plugins are not implemented. |
| `POST /plugins/{name}/upgrade` | Todo | Handler currently returns an empty success; plugins are not implemented. Minimum API version 1.26. |
| `POST /plugins/{name}/set` | Todo | Handler currently returns an empty success; plugins are not implemented. |
| `POST /plugins/create` | Todo | Handler currently returns an empty success; plugins are not implemented. |
| `GET /containers/{name}/checkpoints` | Todo | Route matches Docker's list method/path, but the handler is still a no-op and returns no checkpoint list. |
| `POST /containers/{name}/checkpoints` | Todo | Route matches Docker's create method/path, but the handler is still a no-op. Minimum API version 1.31. |
| `DELETE /containers/{name}/checkpoints/{checkpoint}` | Todo | Route matches Docker's delete method/path, but the handler is still a no-op. |
| `GET /distribution/{name}/json` | Partial | Queries the upstream registry using request credentials and returns manifest/index digest, media type, size and platforms. Uses HTTPS except for loopback registries; schema1 and registry mirrors are not supported. Minimum API version 1.30. |
| `POST /session` | Partial | Implements the h2c upgrade and proxies session services to configured external BuildKit, including scoped build auth. Builds must use the same Dink/backend replicas. |
| `POST /grpc` | Partial | Implements the h2c upgrade; native HTTP/2 gRPC is also supported with identity/auth middleware. Forwards build control, progress and scoped frontend calls; translates tagged `moby` exports into Dinki publication. Build history listing, streaming, record updates and referenced content reads are identity-scoped. Cache administration, general content mutation, history archive imports and advanced build modes remain unsupported. See [BuildKit integration](buildkit.md). |

## Debug

These are Go process diagnostics, not Docker resource endpoints. They delegate to
the Go standard library handlers; restrict access appropriately because they
can expose process data or consume CPU while profiling.

| Method and endpoint | Status | Caveats |
| --- | --- | --- |
| `GET /debug/vars` | 100% | Serves Go `expvar` data for the Dink process. |
| `GET /debug/pprof/` | 100% | Serves the Go pprof index. |
| `GET /debug/pprof/cmdline` | 100% | Serves the Dink process command line. |
| `GET /debug/pprof/profile` | 100% | Captures a Go CPU profile; profiling may consume CPU. |
| `GET /debug/pprof/symbol` | 100% | Serves Go pprof symbol lookup. |
| `GET /debug/pprof/trace` | 100% | Captures a Go runtime trace. |
| `GET /debug/pprof/{name}` | 100% | Serves profiles recognized by Go's pprof handler; profile availability depends on the runtime. |