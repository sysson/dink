#!/bin/sh
set -eu
if [ "$#" -ne 5 ]; then
    echo "usage: $0 TAG COMMIT BUNDLE_DIRECTORY NOTES_FILE ALREADY_PUBLISHED" >&2
    exit 1
fi
tag=$1
commit=$2
bundle=$3
notes=$4
published=$5
sh scripts/validate-release-tag.sh "$tag"
sh scripts/verify-release-bundle.sh "$bundle"
test "$(cat "$bundle/version.txt")" = "$tag"
test "$(cat "$bundle/commit.txt")" = "$commit"
test "$(git rev-parse "$tag^{commit}")" = "$commit"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
trap 'exit 1' INT TERM

for image in dink dinki; do
    digest=$(awk -v image="$image" '$1 == image {print $2}' "$bundle/images.txt")
    docker buildx imagetools inspect --raw "ghcr.io/sysson/$image@$digest" > "$work/$image.json"
    for arch in amd64 arm64; do
        jq -e --arg arch "$arch" '.manifests | any(.platform.os == "linux" and .platform.architecture == $arch)' "$work/$image.json" > /dev/null
    done
done

if [ "$published" != true ]; then
    # List first, so a network/authentication error cannot be mistaken for absence.
    gh api --paginate "repos/{owner}/{repo}/releases" --jq '.[] | select(.tag_name == "'"$tag"'") | [.id, .draft] | @tsv' > "$work/release"
    if [ -s "$work/release" ]; then
        test "$(awk '{print $2}' "$work/release")" = true || {
            echo "release is already public; refusing to replace its assets" >&2
            exit 1
        }
    else
        git push origin "refs/tags/$tag"
        case "$tag" in
            *-*) gh release create "$tag" --verify-tag --draft --prerelease --title "$tag" --notes-file "$notes" ;;
            *) gh release create "$tag" --verify-tag --draft --title "$tag" --notes-file "$notes" ;;
        esac
    fi
    gh release view "$tag" --json assets --jq '.assets[].name' > "$work/assets"
    for file in checksums.txt install.yaml.sha256 dinkle_linux_amd64.tar.gz dinkle_linux_arm64.tar.gz dinkle_darwin_amd64.tar.gz dinkle_darwin_arm64.tar.gz dinkle_windows_amd64.zip dinkle_windows_arm64.zip install.yaml images.txt version.txt commit.txt; do
        if grep -Fxq "$file" "$work/assets"; then
            gh release download "$tag" --pattern "$file" --dir "$work"
            cmp "$bundle/$file" "$work/$file" || {
                echo "conflicting draft asset $file; refusing to overwrite it" >&2
                exit 1
            }
        else
            gh release upload "$tag" "$bundle/$file"
        fi
    done
    gh release download "$tag" --dir "$work/verified"
    sh scripts/verify-release-bundle.sh "$work/verified"
fi

for image in dink dinki; do
    digest=$(awk -v image="$image" '$1 == image {print $2}' "$bundle/images.txt")
    # Registry tag lookup is authenticated and must distinguish 404 from other failures.
    token=$(curl -fsS -u "$GITHUB_ACTOR:$GH_TOKEN" "https://ghcr.io/token?service=ghcr.io&scope=repository:sysson/$image:pull" | jq -er .token)
    status=$(curl -sS -o "$work/tag-$image" -w '%{http_code}' \
        -H "Authorization: Bearer $token" \
        -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json' \
        "https://ghcr.io/v2/sysson/$image/manifests/$tag")
    case "$status" in
        200)
            actual="sha256:$(sha256sum "$work/tag-$image" | cut -d ' ' -f 1)"
            test "$actual" = "$digest" || { echo "conflicting image tag $image:$tag" >&2; exit 1; }
            ;;
        404)
            test "$published" != true || { echo "published image tag $image:$tag is missing" >&2; exit 1; }
            docker buildx imagetools create --prefer-index=false --tag "ghcr.io/sysson/$image:$tag" "ghcr.io/sysson/$image@$digest"
            status=$(curl -sS -o "$work/tag-$image" -w '%{http_code}' \
                -H "Authorization: Bearer $token" \
                -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json' \
                "https://ghcr.io/v2/sysson/$image/manifests/$tag")
            test "$status" = 200 || { echo "new image tag lookup failed with HTTP $status" >&2; exit 1; }
            actual="sha256:$(sha256sum "$work/tag-$image" | cut -d ' ' -f 1)"
            test "$actual" = "$digest" || { echo "new image tag does not preserve digest $image:$tag" >&2; exit 1; }
            ;;
        *) echo "image tag lookup failed with HTTP $status" >&2; exit 1 ;;
    esac
done

if [ "$published" != true ]; then
    gh release edit "$tag" --draft=false
    # A retry of an older public release must never move latest backwards.
    for image in dink dinki; do
        digest=$(awk -v image="$image" '$1 == image {print $2}' "$bundle/images.txt")
        docker buildx imagetools create --prefer-index=false --tag "ghcr.io/sysson/$image:latest" "ghcr.io/sysson/$image@$digest"
    done
fi
