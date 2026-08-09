#!/usr/bin/env bash
set -euo pipefail

sudo apt-get update -y
sudo apt-get install -y --no-install-recommends ca-certificates curl jq unzip git build-essential

if ! command -v protoc >/dev/null 2>&1; then
  URL=$(curl -s https://api.github.com/repos/protocolbuffers/protobuf/releases/latest \
  | jq -r '.assets[] | select(.name | endswith("linux-x86_64.zip")) | .browser_download_url')
  curl -LO $URL
  sudo unzip -o $(basename $URL) -d /usr/local/bin/protoc

  go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
fi

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
