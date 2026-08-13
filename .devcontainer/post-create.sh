#!/usr/bin/env bash
set -euo pipefail

sudo apt-get update -y
sudo apt-get install -y --no-install-recommends ca-certificates curl

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

CLUSTER_NAME="lumine-dev"
CONFIG_FILE=".devcontainer/dev-cluster.yaml"

API_HOST=$(getent hosts host.docker.internal | awk '{print $1}')
if [[ -z "${API_HOST}" ]]; then
  echo "host.docker.internal not resolvable — falling back to 172.17.0.1"
  API_HOST="172.17.0.1"
fi

if kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
  echo "Kind cluster '${CLUSTER_NAME}' already exists."
  exit 0
fi

TMP_CONFIG=$(mktemp)
sed "s/apiServerAddress: \".*\"/apiServerAddress: \"${API_HOST}\"/" "${CONFIG_FILE}" > "${TMP_CONFIG}"

kind create cluster --name "${CLUSTER_NAME}" --config "${TMP_CONFIG}"