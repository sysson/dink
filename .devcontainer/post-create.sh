#!/usr/bin/env bash
set -euo pipefail

sudo mkdir -p ${DINK_CONFIG:-$HOME/.config/dink}
sudo chown -R vscode:vscode ${DINK_CONFIG:-$HOME/.config/dink}

sudo install -d -o vscode -g vscode "$HOME/.cache" "$HOME/.cache/go-build" "$(go env GOPATH)/pkg/mod"

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