#!/usr/bin/env bash
set -euo pipefail

sudo apt-get update -y
sudo apt-get install -y --no-install-recommends ca-certificates curl openssl

go install github.com/bufbuild/buf/cmd/buf@latest

if ! command -v golangci-lint >/dev/null 2>&1; then
  curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b $(go env GOPATH)/bin v2.12.2
fi

# ko builds the dink image straight from the Go module, so no Dockerfile is
# needed for the inner loop.
go install github.com/google/ko@latest

if ! command -v tilt >/dev/null 2>&1; then
  curl -fsSL https://raw.githubusercontent.com/tilt-dev/tilt/master/scripts/install.sh | bash
fi

CLUSTER_NAME="dink-dev"

minikube start -p "${CLUSTER_NAME}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
"${REPO_ROOT}/deploy/gen-certs.sh"
