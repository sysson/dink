#!/bin/sh
set -eu
root=$(pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
trap 'exit 1' INT TERM
mkdir -p "$work/bin" "$work/bundle"
export TEST_WORK="$work"
export TEST_LOG="$work/commands"
export PATH="$work/bin:$PATH"
export GITHUB_ACTOR=test GH_TOKEN=test
printf '%s' '{"manifests":[{"platform":{"os":"linux","architecture":"amd64"}},{"platform":{"os":"linux","architecture":"arm64"}}]}' > "$work/index"
digest="sha256:$(sha256sum "$work/index" | cut -d ' ' -f 1)"
printf 'fixture\n' > "$work/dinkle"
printf 'fixture\n' > "$work/dinkle.exe"
for os in linux darwin; do
    for arch in amd64 arm64; do
        tar -czf "$work/bundle/dinkle_${os}_${arch}.tar.gz" -C "$work" dinkle
    done
done
for arch in amd64 arm64; do
    (cd "$work" && zip -q "$work/bundle/dinkle_windows_${arch}.zip" dinkle.exe)
done
printf 'dink %s\ndinki %s\n' "$digest" "$digest" > "$work/bundle/images.txt"
printf 'image: ghcr.io/sysson/dink@%s\nimage: ghcr.io/sysson/dinki@%s\n' "$digest" "$digest" > "$work/bundle/install.yaml"
printf 'v1.2.3\n' > "$work/bundle/version.txt"
printf 'fixture-commit\n' > "$work/bundle/commit.txt"
(cd "$work/bundle" && sha256sum install.yaml > install.yaml.sha256 && sha256sum dinkle_* install.yaml images.txt version.txt commit.txt > checksums.txt)
printf 'release notes\n' > "$work/notes"

cat > "$work/bin/git" <<'EOF'
#!/bin/sh
set -eu
echo "git $*" >> "$TEST_LOG"
if [ "$1" = rev-parse ]; then echo fixture-commit; fi
EOF
cat > "$work/bin/docker" <<'EOF'
#!/bin/sh
set -eu
echo "docker $*" >> "$TEST_LOG"
if [ "$3" = inspect ]; then cat "$TEST_WORK/index"; fi
if [ "$3" = create ]; then
    case "$*" in
        *ghcr.io/sysson/dink:v1.2.3*) touch "$TEST_WORK/tag-dink" ;;
        *ghcr.io/sysson/dinki:v1.2.3*) touch "$TEST_WORK/tag-dinki" ;;
    esac
fi
EOF
cat > "$work/bin/curl" <<'EOF'
#!/bin/sh
set -eu
case "$*" in
    *ghcr.io/token*) echo '{"token":"fixture"}' ;;
    *)
        case "$*" in
            *'/sysson/dink/manifests/'*) image=dink ;;
            *'/sysson/dinki/manifests/'*) image=dinki ;;
            *) echo "unexpected registry URL" >&2; exit 1 ;;
        esac
        output=
        while [ "$#" -gt 0 ]; do
            if [ "$1" = -o ]; then output=$2; shift; fi
            shift
        done
        if [ "$TEST_SCENARIO" = draft ] && [ ! -f "$TEST_WORK/tag-$image" ]; then
            : > "$output"
            printf 404
        elif [ "$TEST_SCENARIO" = http-error ]; then
            : > "$output"
            printf 503
        elif [ "$TEST_SCENARIO" = conflict ]; then
            printf 'conflict' > "$output"
            printf 200
        else
            cp "$TEST_WORK/index" "$output"
            printf 200
        fi
        ;;
esac
EOF
cat > "$work/bin/gh" <<'EOF'
#!/bin/sh
set -eu
echo "gh $*" >> "$TEST_LOG"
case "$*" in
    'api '*)
        if [ "$TEST_SCENARIO" = asset-conflict ]; then printf '123\ttrue\n'; fi
        ;;
    'release view '*)
        if [ "$TEST_SCENARIO" = asset-conflict ]; then echo checksums.txt; fi
        ;;
    'release download '*)
        destination=
        while [ "$#" -gt 0 ]; do
            if [ "$1" = --dir ]; then destination=$2; shift; fi
            shift
        done
        mkdir -p "$destination"
        cp "$TEST_WORK"/bundle/* "$destination/"
        if [ "$TEST_SCENARIO" = asset-conflict ]; then printf 'conflict' > "$destination/checksums.txt"; fi
        ;;
    'release create '*|'release upload '*|'release edit '*) ;;
    *) echo "unexpected gh command: $*" >&2; exit 1 ;;
esac
EOF
chmod +x "$work/bin/git" "$work/bin/docker" "$work/bin/curl" "$work/bin/gh"

export TEST_SCENARIO=draft
sh "$root/scripts/publish-release.sh" v1.2.3 fixture-commit "$work/bundle" "$work/notes" false > "$work/output"
grep -Fq 'gh release create v1.2.3 --verify-tag --draft' "$TEST_LOG"
grep -Fq 'gh release edit v1.2.3 --draft=false' "$TEST_LOG"
test "$(grep -c 'gh release upload' "$TEST_LOG")" -eq 12
# Publication must follow all uploads and both versioned image tags.
awk '/gh release upload/ { if (published) exit 1 }
     /--tag ghcr.io\/sysson\/.*:v1.2.3/ { tags++ }
     /gh release edit/ { if (tags != 2) exit 1; published=1 }
     END { if (!published) exit 1 }' "$TEST_LOG"

: > "$TEST_LOG"
export TEST_SCENARIO=published
sh "$root/scripts/publish-release.sh" v1.2.3 fixture-commit "$work/bundle" "$work/notes" true > "$work/output"
if grep -Eq 'gh release (create|upload|edit)|docker buildx imagetools create|git push' "$TEST_LOG"; then
    echo "public-release verification mutated artifacts" >&2
    exit 1
fi

: > "$TEST_LOG"
export TEST_SCENARIO=conflict
if sh "$root/scripts/publish-release.sh" v1.2.3 fixture-commit "$work/bundle" "$work/notes" false > "$work/output" 2>&1; then
    echo "conflicting image tag was accepted" >&2
    exit 1
fi
if grep -Fq 'gh release edit' "$TEST_LOG"; then
    echo "published despite conflicting image tag" >&2
    exit 1
fi
for scenario in asset-conflict http-error; do
    : > "$TEST_LOG"
    export TEST_SCENARIO="$scenario"
    if sh "$root/scripts/publish-release.sh" v1.2.3 fixture-commit "$work/bundle" "$work/notes" false > "$work/output" 2>&1; then
        echo "accepted $scenario" >&2
        exit 1
    fi
    if grep -Fq 'gh release edit' "$TEST_LOG"; then
        echo "published despite $scenario" >&2
        exit 1
    fi
done
echo "Draft publication, public verification, conflict, and HTTP failure tests passed"
