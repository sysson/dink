#!/usr/bin/env bash

set -euo pipefail

if [[ $# -ne 1 ]]; then
	printf 'usage: %s IMAGE_REF\n' "$0" >&2
	exit 2
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image_ref="$1"

docker build --target dev --tag "${image_ref}" "${repo_root}"
minikube --profile dink-dev image load "${image_ref}"