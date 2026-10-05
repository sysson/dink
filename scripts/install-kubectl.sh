#!/bin/sh
set -eu
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
trap 'exit 1' INT TERM
curl -fsSLo "$work/kubectl" https://dl.k8s.io/release/v1.37.0/bin/linux/amd64/kubectl
curl -fsSLo "$work/kubectl.sha256" https://dl.k8s.io/release/v1.37.0/bin/linux/amd64/kubectl.sha256
(cd "$work" && printf '%s  kubectl\n' "$(cat kubectl.sha256)" | sha256sum -c -)
sudo install -m 0755 "$work/kubectl" /usr/local/bin/kubectl
