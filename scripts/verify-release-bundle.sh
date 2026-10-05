#!/bin/sh
set -eu
if [ "$#" -ne 1 ]; then
    echo "usage: $0 BUNDLE_DIRECTORY" >&2
    exit 1
fi
cd "$1"
expected="dinkle_linux_amd64.tar.gz dinkle_linux_arm64.tar.gz dinkle_darwin_amd64.tar.gz dinkle_darwin_arm64.tar.gz dinkle_windows_amd64.zip dinkle_windows_arm64.zip install.yaml images.txt version.txt commit.txt"
for file in $expected; do
    test -s "$file" || { echo "missing or empty release asset: $file" >&2; exit 1; }
done
test -s checksums.txt
test -s install.yaml.sha256
# Restrict checksum paths before letting sha256sum open downloaded filenames.
awk '
    NF != 2 || length($1) != 64 || $1 !~ /^[0-9a-f]+$/ || $2 !~ /^(dinkle_(linux|darwin)_(amd64|arm64)\.tar\.gz|dinkle_windows_(amd64|arm64)\.zip|install\.yaml|images\.txt|version\.txt|commit\.txt)$/ { exit 1 }
    seen[$2]++ { exit 1 }
    END { if (NR != 10) exit 1 }
' checksums.txt
sha256sum -c checksums.txt
awk 'NF != 2 || length($1) != 64 || $1 !~ /^[0-9a-f]+$/ || $2 != "install.yaml" { exit 1 } END { if (NR != 1) exit 1 }' install.yaml.sha256
sha256sum -c install.yaml.sha256
for os in linux darwin; do
    for arch in amd64 arm64; do
        tar -tzf "dinkle_${os}_${arch}.tar.gz" | grep -Fxq dinkle
        test "$(tar -xOzf "dinkle_${os}_${arch}.tar.gz" dinkle | wc -c)" -gt 0
    done
done
for arch in amd64 arm64; do
    unzip -tqq "dinkle_windows_${arch}.zip"
    test "$(unzip -p "dinkle_windows_${arch}.zip" dinkle.exe | wc -c)" -gt 0
done
test "$(wc -l < images.txt)" -eq 2
for image in dink dinki; do
    digest=$(awk -v image="$image" '$1 == image && NF == 2 {print $2}' images.txt)
    printf '%s\n' "$digest" | grep -Eq '^sha256:[0-9a-f]{64}$'
    grep -Fq "image: ghcr.io/sysson/$image@$digest" install.yaml
done
