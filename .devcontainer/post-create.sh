#!/usr/bin/env bash
set -euo pipefail

sudo install -d -o vscode -g vscode /go/pkg/mod /go/pkg/sumdb /home/vscode/.cache /home/vscode/.cache/go-build /home/vscode/.config/dink

go mod download
(cd sdk && go mod download)

make bootstrap

echo "Post-create script completed successfully."
echo "Run 'make dev' to start the development environment."