#!/usr/bin/env bash
set -euo pipefail

sudo install -d -o vscode -g vscode /go/pkg/mod /go/pkg/sumdb /home/vscode/.cache/go-build /home/vscode/.config/dink

sudo apt-get update -y
sudo apt-get install -y --no-install-recommends ca-certificates curl

go install github.com/bufbuild/buf/cmd/buf@latest

if ! command -v tilt >/dev/null 2>&1; then
  curl -fsSL https://raw.githubusercontent.com/tilt-dev/tilt/master/scripts/install.sh | bash
fi

docker context create --docker host=unix:///var/run/docker.sock minikube >/dev/null 2>&1 || true
make bootstrap

echo "Post-create script completed successfully."
echo "Run 'make dev' to start the development environment."