#!/bin/sh
set -eu

if [ "$#" -ne 3 ]; then
    echo "usage: $0 DINK_DIGEST DINKI_DIGEST OUTPUT_DIRECTORY" >&2
    exit 1
fi
for digest in "$1" "$2"; do
    if ! printf '%s\n' "$digest" | grep -Eq '^sha256:[0-9a-f]{64}$'; then
        echo "invalid image digest: $digest" >&2
        exit 1
    fi
done

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
trap 'exit 1' INT TERM
cp -R deploy "$work/deploy"
cat > "$work/kustomization.yaml" <<EOF
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deploy
images:
  - name: ghcr.io/sysson/dink
    digest: $1
  - name: ghcr.io/sysson/dinki
    digest: $2
EOF
mkdir -p "$3"
kubectl kustomize "$work" > "$3/install.yaml"
for image in "ghcr.io/sysson/dink@$1" "ghcr.io/sysson/dinki@$2"; do
    if ! grep -Fq "image: $image" "$3/install.yaml"; then
        echo "rendered manifest is missing pinned image $image" >&2
        exit 1
    fi
done
(cd "$3" && sha256sum install.yaml > install.yaml.sha256)
