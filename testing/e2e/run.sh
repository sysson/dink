#!/usr/bin/env bash
set -euo pipefail

if [[ ${1:-} == --help ]]; then
  cat <<'EOF'
Usage: bash testing/e2e/run.sh

Creates a disposable Minikube/containerd cluster, deploys this checkout, runs
real Docker/Buildx/Compose/Swarm and selected upstream CLI tests, then deletes it.
Requires Docker, its Buildx and Compose plugins, Minikube, kubectl, Go and git.

Settings:
  E2E_HOST_CONTEXT       Real Docker daemon context (default: default)
  E2E_MEMORY             Minikube memory in MiB (default: 4096)
  E2E_CPUS               Minikube CPUs (default: 2)
  E2E_MTU                Test Docker bridge MTU (default: 1280)
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
for tool in docker minikube kubectl go git; do
  command -v "$tool" >/dev/null || { echo "Required tool missing: $tool" >&2; exit 1; }
done
case ${E2E_UPSTREAM:-1} in
  0|1) ;;
  *) echo "E2E_UPSTREAM must be 0 or 1" >&2; exit 2 ;;
esac
mtu=${E2E_MTU:-1280}
if [[ ! $mtu =~ ^[0-9]{3,4}$ ]] || (( 10#$mtu < 1280 || 10#$mtu > 9000 )); then
  echo "E2E_MTU must be an integer between 1280 and 9000" >&2
  exit 2
fi

host_context=${E2E_HOST_CONTEXT:-default}
unset DOCKER_HOST DOCKER_TLS_VERIFY DOCKER_CERT_PATH DOCKER_API_VERSION
unset BUILDX_BUILDER BUILDX_CONFIG BUILDX_HOST BUILDX_EXPERIMENTAL
export DOCKER_CONTEXT="$host_context"
docker info >/dev/null
docker buildx version
docker compose version

state=$(mktemp -d "${TMPDIR:-/tmp}/dink-e2e-state.XXXXXXXX")
logs=""
cluster_started=0
network_created=0
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
    if ! minikube delete -p "$profile"; then
      echo "Cluster deletion failed. State retained at $state." >&2
      echo "Retry: MINIKUBE_HOME=$state/minikube KUBECONFIG=$state/kubeconfig DOCKER_CONFIG=$state/docker DOCKER_CONTEXT=e2e-host minikube delete -p $profile" >&2
      echo "Logs: $logs"
      exit 1
    fi
  fi
  if [[ $network_created == 1 ]]; then
    local network_id
    if ! network_id=$(docker --context e2e-host network ls --filter "name=^${profile}$" --format '{{.ID}}'); then
      echo "Unable to inspect test network. State retained at $state." >&2
      exit 1
    fi
    if [[ -n $network_id ]] && ! docker --context e2e-host network rm "$profile"; then
      echo "Test network deletion failed. State retained at $state." >&2
      echo "Retry: DOCKER_CONFIG=$state/docker docker --context e2e-host network rm $profile" >&2
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
export MINIKUBE_HOME="$state/minikube"
export DINK_CERTS_DIR="$state/certs"

go build -o "$state/dinkle" ./cmd/dinkle
network_created=1
docker network create --driver=bridge --opt "com.docker.network.driver.mtu=$mtu" "$profile"
cluster_started=1
minikube start -p "$profile" --driver=docker --container-runtime=containerd \
  --network="$profile" --cni=calico --keep-context --embed-certs \
  --memory="${E2E_MEMORY:-4096}" --cpus="${E2E_CPUS:-2}" \
  --kubernetes-version="${E2E_KUBERNETES_VERSION:-v1.37.0}" --wait=all --wait-timeout=5m
kubectl config use-context "$profile"
kubectl wait --for=condition=Ready nodes --all --timeout=180s
kubectl rollout status deployment/coredns --namespace=kube-system --timeout=180s
minikube ssh -p "$profile" -- 'sudo containerd config dump' >"$logs/containerd-config.log"
if ! grep -Eq 'config_path = .*/etc/containerd/certs.d' "$logs/containerd-config.log"; then
  echo "containerd is not configured to use /etc/containerd/certs.d; Dinki pulls would bypass registry trust." >&2
  exit 1
fi

for component in dink dinki; do
  docker build --target "$component" -t "ghcr.io/sysson/$component:$profile" .
  minikube image load -p "$profile" "ghcr.io/sysson/$component:$profile"
done
"$state/dinkle" ca generate
node_ip=$(minikube ip -p "$profile")
"$state/dinkle" server issue --ip "$node_ip"
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

endpoint="tcp://$node_ip:32376"
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
export DINK_INTEGRATION_BUILDKIT_ADDR="tcp://$node_ip:32374"
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
