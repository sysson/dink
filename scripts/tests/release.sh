#!/bin/sh
set -eu
root=$(pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
trap 'exit 1' INT TERM

for tag in v1.2.3 v0.2.1-beta.1; do
    sh scripts/validate-release-tag.sh "$tag"
done
for tag in main v01.2.3 'v1.2.3/../../main' ''; do
    if sh scripts/validate-release-tag.sh "$tag" > /dev/null 2>&1; then
        echo "accepted invalid release tag: $tag" >&2
        exit 1
    fi
done

mkdir -p "$work/bundle"
cd "$work/bundle"
printf 'fixture\n' > "$work/dinkle"
printf 'fixture\n' > "$work/dinkle.exe"
for os in linux darwin; do
    for arch in amd64 arm64; do
        tar -czf "dinkle_${os}_${arch}.tar.gz" -C "$work" dinkle
    done
done
for arch in amd64 arm64; do
    (cd "$work" && zip -q "$work/bundle/dinkle_windows_${arch}.zip" dinkle.exe)
done
dink=sha256:1111111111111111111111111111111111111111111111111111111111111111
dinki=sha256:2222222222222222222222222222222222222222222222222222222222222222
printf 'dink %s\ndinki %s\n' "$dink" "$dinki" > images.txt
printf 'image: ghcr.io/sysson/dink@%s\nimage: ghcr.io/sysson/dinki@%s\n' "$dink" "$dinki" > install.yaml
printf 'v1.2.3\n' > version.txt
printf 'fixture-commit\n' > commit.txt
sha256sum install.yaml > install.yaml.sha256
sha256sum dinkle_* install.yaml images.txt version.txt commit.txt > checksums.txt
sh "$root/scripts/verify-release-bundle.sh" "$work/bundle"
cp checksums.txt "$work/checksums.txt"

printf 'corruption\n' >> install.yaml
if sh "$root/scripts/verify-release-bundle.sh" "$work/bundle" > /dev/null 2>&1; then
    echo "accepted corrupted manifest" >&2
    exit 1
fi
printf 'image: ghcr.io/sysson/dink@%s\nimage: ghcr.io/sysson/dinki@%s\n' "$dink" "$dinki" > install.yaml
rm dinkle_linux_arm64.tar.gz
if sh "$root/scripts/verify-release-bundle.sh" "$work/bundle" > /dev/null 2>&1; then
    echo "accepted incomplete platform bundle" >&2
    exit 1
fi
tar -czf dinkle_linux_arm64.tar.gz -C "$work" dinkle
sed 's/install.yaml/..\/outside.yaml/' "$work/checksums.txt" > checksums.txt
if sh "$root/scripts/verify-release-bundle.sh" "$work/bundle" > /dev/null 2>&1; then
    echo "accepted unsafe checksum path" >&2
    exit 1
fi
cp "$work/checksums.txt" checksums.txt
sed 's/ghcr.io\/sysson\/dink@/ghcr.io\/sysson\/wrong@/' install.yaml > "$work/manifest"
cp "$work/manifest" install.yaml
sha256sum install.yaml > install.yaml.sha256
sha256sum dinkle_* install.yaml images.txt version.txt commit.txt > checksums.txt
if sh "$root/scripts/verify-release-bundle.sh" "$work/bundle" > /dev/null 2>&1; then
    echo "accepted manifest with wrong image reference" >&2
    exit 1
fi
echo "Release bundle validation tests passed"
