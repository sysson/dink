#!/usr/bin/env bash
set -euo pipefail

sudo apt-get update -y
sudo apt-get install -y --no-install-recommends ca-certificates curl

go install github.com/bufbuild/buf/cmd/buf@latest

if ! command -v tilt >/dev/null 2>&1; then
  curl -fsSL https://raw.githubusercontent.com/tilt-dev/tilt/master/scripts/install.sh | bash
fi

make start
make certs
