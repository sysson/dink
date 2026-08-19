#!/usr/bin/env bash
set -euo pipefail

sudo apt-get update -y
sudo apt-get install -y --no-install-recommends ca-certificates curl openssl

go install github.com/bufbuild/buf/cmd/buf@latest

if ! command -v golangci-lint >/dev/null 2>&1; then
  curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b $(go env GOPATH)/bin v2.12.2
fi

if ! command -v kubectl >/dev/null 2>&1; then
  curl -fsSL "https://dl.k8s.io/release/$(curl -fsSL https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" -o /tmp/kubectl
  sudo install -m 0755 /tmp/kubectl /usr/local/bin/kubectl
fi

if ! command -v kind >/dev/null 2>&1; then
  curl -fsSL https://kind.sigs.k8s.io/dl/v0.24.0/kind-linux-amd64 -o /tmp/kind
  sudo install -m 0755 /tmp/kind /usr/local/bin/kind
fi

CLUSTER_NAME="dink-dev"
CONFIG_FILE=".devcontainer/dev-cluster.yaml"
# Dedicated kubeconfig, scoped to only this cluster, living in the (bind-mounted)
# workspace so it survives container rebuilds without needing to mount $HOME/.kube.
KUBECONFIG_PATH=".devcontainer/.kube/config"

API_HOST=$(getent hosts host.docker.internal | awk '{print $1}')
if [[ -z "${API_HOST}" ]]; then
  echo "host.docker.internal not resolvable — falling back to 172.17.0.1"
  API_HOST="172.17.0.1"
fi

mkdir -p "$(dirname "${KUBECONFIG_PATH}")"

if kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
  echo "Kind cluster '${CLUSTER_NAME}' already exists."
else
  TMP_CONFIG=$(mktemp)
  sed "s/apiServerAddress: \".*\"/apiServerAddress: \"${API_HOST}\"/" "${CONFIG_FILE}" > "${TMP_CONFIG}"
  kind create cluster --name "${CLUSTER_NAME}" --config "${TMP_CONFIG}"
fi

# Always (re)write the kubeconfig: the container's $HOME is ephemeral across
# rebuilds even though the kind cluster (running on the host's docker daemon)
# persists, so a fresh container otherwise loses access to it.
kind export kubeconfig --name "${CLUSTER_NAME}" --kubeconfig "${KUBECONFIG_PATH}"

# Dev-only TLS material for exercising dink's TLS/mTLS server flags locally.
# Never committed — see .gitignore — and regenerated only if missing, so it
# survives container rebuilds via the bind-mounted workspace.
CERTS_DIR=".devcontainer/certs"
CA_CERT="${CERTS_DIR}/ca.crt"
CA_KEY="${CERTS_DIR}/ca.key"
SERVER_CERT="${CERTS_DIR}/server.crt"
SERVER_KEY="${CERTS_DIR}/server.key"
CLIENT_NAMES=(testing staging)

if [[ -f "${SERVER_CERT}" && -f "${SERVER_KEY}" && -f "${CA_CERT}" ]]; then
  echo "Dev TLS material already present in ${CERTS_DIR}."
else
  mkdir -p "${CERTS_DIR}"
  WORKDIR=$(mktemp -d)
  trap 'rm -rf "${WORKDIR}"' EXIT

  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
    -keyout "${CA_KEY}" -out "${CA_CERT}" -days 3650 -subj "/CN=dink-dev CA"

  cat > "${WORKDIR}/server-ext.cnf" <<EOF
subjectAltName = DNS:localhost,DNS:dink,IP:127.0.0.1,DNS:host.docker.internal
EOF
  openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
    -keyout "${SERVER_KEY}" -out "${WORKDIR}/server.csr" -subj "/CN=dink"
  openssl x509 -req -in "${WORKDIR}/server.csr" -CA "${CA_CERT}" -CAkey "${CA_KEY}" -CAcreateserial \
    -out "${SERVER_CERT}" -days 397 -extfile "${WORKDIR}/server-ext.cnf"

  for name in "${CLIENT_NAMES[@]}"; do
    openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
      -keyout "${CERTS_DIR}/client-${name}.key" -out "${WORKDIR}/client-${name}.csr" \
      -subj "/O=${name}/CN=dink-client-${name}"
    openssl x509 -req -in "${WORKDIR}/client-${name}.csr" -CA "${CA_CERT}" -CAkey "${CA_KEY}" -CAcreateserial \
      -out "${CERTS_DIR}/client-${name}.crt" -days 90
  done

  echo "Generated dev CA, server cert, and client certs (${CLIENT_NAMES[*]}) in ${CERTS_DIR}."
fi
