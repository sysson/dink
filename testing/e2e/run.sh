#!/usr/bin/env bash
set -euo pipefail

if [[ ${1:-} == --help ]]; then
  cat <<'EOF'
Usage: bash testing/e2e/run.sh

Creates a disposable Kind/containerd cluster, deploys this checkout, runs
real Docker/Buildx/Compose/Swarm and selected upstream CLI tests, then deletes it.
Requires Docker, its Buildx and Compose plugins, Kind, kubectl, Go and git.

Settings:
  E2E_HOST_CONTEXT       Real Docker daemon context (default: default)
  E2E_MEMORY             Kind node memory in MiB (default: 4096)
  E2E_CPUS               Kind node CPUs (default: 2)
  E2E_KUBERNETES_VERSION Kubernetes version (default: v1.37.0)
  E2E_UPSTREAM            Run pinned upstream CLI tests (default: 1; set 0 to skip)

Logs are retained in a printed temporary directory. Certificates, client config,
cluster state and upstream checkout are removed. Existing contexts are untouched.
EOF
  exit 0
fi
if [[ $# != 0 ]]; then
  echo "Unexpected argument; use --help." >&2
  exit 2
fi

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"
for tool in docker kind kubectl go git; do
  command -v "$tool" >/dev/null || { echo "Required tool missing: $tool" >&2; exit 1; }
done
case ${E2E_UPSTREAM:-1} in
  0|1) ;;
  *) echo "E2E_UPSTREAM must be 0 or 1" >&2; exit 2 ;;
esac
memory=${E2E_MEMORY:-4096}
if [[ ! $memory =~ ^[0-9]+$ ]] || (( memory < 2048 )); then
  echo "E2E_MEMORY must be an integer of at least 2048 MiB" >&2
  exit 2
fi
cpus=${E2E_CPUS:-2}
if [[ ! $cpus =~ ^[0-9]+([.][0-9]+)?$ ]] || ! awk -v cpus="$cpus" 'BEGIN { exit !(cpus > 0) }'; then
  echo "E2E_CPUS must be a positive number" >&2
  exit 2
fi

host_context=${E2E_HOST_CONTEXT:-default}
unset DOCKER_HOST DOCKER_TLS_VERIFY DOCKER_CERT_PATH DOCKER_API_VERSION
unset BUILDX_BUILDER BUILDX_CONFIG BUILDX_HOST BUILDX_EXPERIMENTAL
unset KIND_EXPERIMENTAL_DOCKER_NETWORK
export DOCKER_CONTEXT="$host_context"
docker info >/dev/null
docker buildx version
docker compose version

state=$(mktemp -d "${TMPDIR:-/tmp}/dink-e2e-state.XXXXXXXX")
logs=""
cluster_started=0
cleanup() {
  local status=$?
  trap - EXIT
  trap '' INT TERM
  set +e
  if [[ $cluster_started == 1 ]]; then
    kubectl --request-timeout=10s get pods,pvc,svc -A -o wide >"$logs/resources.log" 2>&1
    kubectl --request-timeout=10s get events -A --sort-by=.metadata.creationTimestamp >"$logs/events.log" 2>&1
    kubectl --request-timeout=10s describe pods -A >"$logs/pods.log" 2>&1
    for app in dink dinki dinki-node buildkit; do
      kubectl --request-timeout=10s -n dink-system logs \
        -l "app.kubernetes.io/name=$app" --all-containers --prefix --tail=1000 \
        >"$logs/$app.log" 2>&1
    done
    kubectl --request-timeout=10s -n dink-system logs deployment/e2e-registry --tail=1000 \
      >"$logs/e2e-registry.log" 2>&1
  fi
  if [[ $cluster_started == 1 ]]; then
    if ! kind delete cluster --name "$profile"; then
      echo "Cluster deletion failed. State retained at $state." >&2
      echo "Retry: KUBECONFIG=$state/kubeconfig DOCKER_CONFIG=$state/docker DOCKER_CONTEXT=e2e-host kind delete cluster --name $profile" >&2
      echo "Logs: $logs"
      exit 1
    fi
  fi
  rm -rf -- "$state"
  echo "Logs: $logs"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
logs=$(mktemp -d "${TMPDIR:-/tmp}/dink-e2e-logs.XXXXXXXX")
profile="dink-e2e-$(basename "$state" | cut -d. -f2 | tr '[:upper:]' '[:lower:]')"
# Ctrl+C reaches tee too; keep the output pipe open until cleanup finishes.
exec > >(trap '' INT TERM; exec tee "$logs/runner.log") 2>&1
echo "Test cluster: $profile"
echo "Logs: $logs"

# Export/import keeps even a TLS host context usable without changing its config.
docker context export "$host_context" "$state/host.dockercontext"
export DOCKER_CONFIG="$state/docker"
mkdir -p "$DOCKER_CONFIG"
docker context import e2e-host "$state/host.dockercontext"
export DOCKER_CONTEXT=e2e-host
export KUBECONFIG="$state/kubeconfig"
export DINK_CERTS_DIR="$state/certs"

go build -o "$state/dinkle" ./cmd/dinkle
kind_version=${E2E_KUBERNETES_VERSION:-v1.37.0}
cat >"$state/kind.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  disableDefaultCNI: true
  podSubnet: 192.168.0.0/16
containerdConfigPatches:
  - |-
    version = 2
    [plugins."io.containerd.grpc.v1.cri".registry]
      config_path = "/etc/containerd/certs.d"
  - |-
    version = 3
    [plugins."io.containerd.cri.v1.images".registry]
      config_path = "/etc/containerd/certs.d"
  - |-
    version = 4
    [plugins."io.containerd.cri.v1.images".registry]
      config_path = "/etc/containerd/certs.d"
nodes:
  - role: control-plane
    extraPortMappings:
      - containerPort: 32374
        hostPort: 32374
        listenAddress: "127.0.0.1"
      - containerPort: 32376
        hostPort: 32376
        listenAddress: "127.0.0.1"
      - containerPort: 32500
        hostPort: 32500
        listenAddress: "127.0.0.1"
EOF
cluster_started=1
kind create cluster --name "$profile" --image "kindest/node:$kind_version" \
  --config "$state/kind.yaml" --kubeconfig "$KUBECONFIG"
api_ready=0
for _ in {1..60}; do
  if kubectl get --raw=/readyz >/dev/null 2>&1; then
    api_ready=1
    break
  fi
  sleep 2
done
if [[ $api_ready != 1 ]]; then
  echo "Kubernetes API did not become ready after Kind cluster creation." >&2
  exit 1
fi
for node in $(kind get nodes --name "$profile"); do
  docker update --memory="${memory}m" --memory-swap="${memory}m" --cpus="$cpus" "$node"
done
kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.33.0/manifests/calico.yaml
kubectl rollout status daemonset/calico-node --namespace=kube-system --timeout=300s
kubectl rollout status deployment/calico-kube-controllers --namespace=kube-system --timeout=300s
kubectl wait --for=condition=Ready nodes --all --timeout=180s
kubectl rollout status deployment/coredns --namespace=kube-system --timeout=180s
node=$(kind get nodes --name "$profile" | head -n1)
docker exec "$node" containerd config dump >"$logs/containerd-config.log"
if ! grep -Eq 'config_path = .*/etc/containerd/certs.d' "$logs/containerd-config.log"; then
  echo "containerd is not configured to use /etc/containerd/certs.d; Dinki pulls would bypass registry trust." >&2
  exit 1
fi

for component in dink dinki; do
  docker build --target "$component" -t "ghcr.io/sysson/$component:$profile" .
  kind load docker-image "ghcr.io/sysson/$component:$profile" --name "$profile"
done
"$state/dinkle" ca generate
host_ip=127.0.0.1
node=$(kind get nodes --name "$profile" | head -n1)
node_ip=$(docker inspect --format '{{(index .NetworkSettings.Networks "kind").IPAddress}}' "$node")
"$state/dinkle" server issue --ip "$host_ip"
"$state/dinkle" server issue --serviceName dinki --serverSecretName dinki-tls
"$state/dinkle" server issue --serviceName buildkit --serverSecretName buildkit-tls
"$state/dinkle" server issue --serviceName e2e-registry --serverSecretName e2e-registry-tls --ip "$node_ip"
go run ./testing/e2e/registry-auth "$state"
kubectl -n dink-system create secret generic e2e-registry-auth --from-file=htpasswd="$state/htpasswd"
kubectl apply -f testing/e2e/registry.yaml
kubectl -n dink-system rollout status deployment/e2e-registry --timeout=180s
registry="$node_ip:32500"

mkdir -p "$state/overlay"
cp -R deploy "$state/deploy"
cat >"$state/overlay/kustomization.yaml" <<EOF
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../deploy
images:
  - name: ghcr.io/sysson/dink
    newTag: $profile
  - name: ghcr.io/sysson/dinki
    newTag: $profile
patches:
  - target:
      kind: Deployment
      name: dink
    patch: |-
      - op: add
        path: /spec/template/spec/containers/0/env
        value:
          - name: SSL_CERT_FILE
            value: /etc/dink/tls/ca.crt
  - target:
      kind: Deployment
      name: dinki
    patch: |-
      - op: add
        path: /spec/template/spec/containers/0/env
        value:
          - name: SSL_CERT_FILE
            value: /etc/dinki/tls/ca.crt
  - target:
      kind: ConfigMap
      name: buildkit-config
    patch: |-
      - op: replace
        path: /data/buildkitd.toml
        value: |
          [worker.oci]
            gc = true
            reservedSpace = "2GiB"
            maxUsedSpace = "10GiB"
            minFreeSpace = "10GiB"
          [registry."dinki.dink-system.svc.cluster.local:5000"]
            ca = ["/etc/buildkit/dinki/ca.crt"]
          [registry."$registry"]
            ca = ["/etc/buildkit/dinki/ca.crt"]
  - target:
      kind: Service
      name: buildkit
    patch: |-
      - op: replace
        path: /spec/type
        value: NodePort
      - op: add
        path: /spec/ports/0/nodePort
        value: 32374
  - target:
      kind: Service
      name: dink
    patch: |-
      - op: add
        path: /spec/type
        value: NodePort
      - op: add
        path: /spec/ports/0/nodePort
        value: 32376
EOF
kubectl apply -k "$state/overlay"
for component in dink dinki buildkit; do
  kubectl -n dink-system rollout status "deployment/$component" --timeout=300s
done
kubectl -n dink-system rollout status daemonset/dinki-node --timeout=180s
for tenant in e2e-a e2e-b; do
  "$state/dinkle" tenant create "$tenant"
done

endpoint="tcp://$host_ip:32376"
for tenant in e2e-a e2e-b; do
  certs="$state/certs/$tenant/default"
  docker context create "$tenant" --docker \
    "host=$endpoint,ca=$certs/ca.pem,cert=$certs/cert.pem,key=$certs/key.pem"
  docker --context "$tenant" version
done

export DINK_INTEGRATION_CONTEXT=e2e-a DINK_INTEGRATION_OTHER_CONTEXT=e2e-b DINK_E2E=1
kubectl -n dink-system get secret buildkit-tls -o 'jsonpath={.data.ca\.crt}' | base64 --decode >"$state/buildkit-ca.pem"
export SSL_CERT_FILE="$state/buildkit-ca.pem"
docker --context e2e-a login "$registry" --username e2e --password-stdin <"$state/registry-password"
export DINK_INTEGRATION_PUSH_REPOSITORY="$registry/buildkit-e2e"
export DINK_INTEGRATION_BUILDKIT_ADDR="tcp://$host_ip:32374"
export DINK_INTEGRATION_BUILDKIT_CA="$state/buildkit-ca.pem"
export DINK_INTEGRATION_BUILDKIT_CERT="$state/certs/e2e-a/default/cert.pem"
export DINK_INTEGRATION_BUILDKIT_KEY="$state/certs/e2e-a/default/key.pem"
export DINK_INTEGRATION_BUILDKIT_SERVER_NAME=buildkit.dink-system.svc.cluster.local
suite_status=0
if ! go test ./testing/integration -run '^(TestE2E|TestBuildKit)' \
  -count=1 -timeout=20m -v; then
  suite_status=1
fi

if [[ ${E2E_UPSTREAM:-1} == 1 ]]; then
  docker --context e2e-a pull alpine:3.21
  docker --context e2e-a tag alpine:3.21 registry:5000/alpine:frozen
  # Deliberately pinned; changing this revision requires reviewing the selection.
  cli_revision=75d1d1a9e62f89d8284ad0b4b8678b936ae5bed9
  git init -q "$state/docker-cli"
  git -C "$state/docker-cli" remote add origin https://github.com/docker/cli.git
  git -C "$state/docker-cli" fetch --depth=1 origin "$cli_revision"
  git -C "$state/docker-cli" checkout -q --detach FETCH_HEAD
  cp "$state/docker-cli/vendor.mod" "$state/docker-cli/go.mod"
  cp "$state/docker-cli/vendor.sum" "$state/docker-cli/go.sum"
  (
    cd "$state/docker-cli"
    unset DOCKER_CONTEXT
    export TEST_DOCKER_HOST="$endpoint"
    export TEST_DOCKER_CERT_PATH="$state/certs/e2e-a/default"
    export TEST_REMOTE_DAEMON=1
    GOWORK=off go test -mod=vendor ./e2e/container \
      -run '^(TestContainerRename|TestContainerRenameEmptyOldName|TestCreateWithEmptySourceVolume|TestCreateWithEmptyVolumeSpec)$' \
      -count=1 -timeout=5m -json >"$logs/upstream-cli.json"
  ) || suite_status=1
  cat "$logs/upstream-cli.json"
  for test in TestContainerRename TestContainerRenameEmptyOldName TestCreateWithEmptySourceVolume TestCreateWithEmptyVolumeSpec; do
    if ! grep -Eq "\"Action\":\"pass\".*\"Test\":\"$test\"" "$logs/upstream-cli.json"; then
      echo "Upstream test did not pass (missing or skipped): $test" >&2
      suite_status=1
    fi
  done
fi
if [[ $suite_status != 0 ]]; then
  echo "Local E2E suite failed; see test output and diagnostics." >&2
  exit "$suite_status"
fi
echo "Local E2E suite passed."
