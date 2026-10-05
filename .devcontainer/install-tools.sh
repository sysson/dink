#!/usr/bin/env bash
set -euo pipefail

arch=$(dpkg --print-architecture)
case "$arch" in
  amd64) buf_arch=x86_64; tilt_arch=x86_64 ;;
  arm64) buf_arch=aarch64; tilt_arch=arm64 ;;
  *) echo "Unsupported devcontainer architecture: $arch" >&2; exit 1 ;;
esac

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
cd "$work"

download() {
  curl --fail --silent --show-error --location --retry 3 "$1" --output "$2"
}

verify() {
  local file=$1 checksums=$2
  awk -v file="$file" '$2 == file { print; found = 1 } END { if (!found) exit 1 }' \
    "$checksums" > selected.sha256
  sha256sum --check selected.sha256
}

kind_file="kind-linux-$arch"
kind_url="https://github.com/kubernetes-sigs/kind/releases/download/v${KIND_VERSION}"
download "$kind_url/$kind_file" "$kind_file"
download "$kind_url/$kind_file.sha256sum" kind.sha256
verify "$kind_file" kind.sha256
install -m 0755 "$kind_file" /usr/local/bin/kind

kubectl_url="https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/linux/$arch/kubectl"
download "$kubectl_url" kubectl
download "$kubectl_url.sha256" kubectl.sha256
printf '%s  kubectl\n' "$(cat kubectl.sha256)" > kubectl-checksums.txt
verify kubectl kubectl-checksums.txt
install -m 0755 kubectl /usr/local/bin/kubectl

buf_file="buf-Linux-$buf_arch"
buf_url="https://github.com/bufbuild/buf/releases/download/v${BUF_VERSION}"
download "$buf_url/$buf_file" "$buf_file"
download "$buf_url/sha256.txt" buf.sha256
verify "$buf_file" buf.sha256
install -m 0755 "$buf_file" /usr/local/bin/buf

tilt_file="tilt.${TILT_VERSION}.linux.${tilt_arch}.tar.gz"
tilt_url="https://github.com/tilt-dev/tilt/releases/download/v${TILT_VERSION}"
download "$tilt_url/$tilt_file" "$tilt_file"
download "$tilt_url/checksums.txt" tilt.sha256
verify "$tilt_file" tilt.sha256
tar -xzf "$tilt_file" tilt
install -m 0755 tilt /usr/local/bin/tilt
