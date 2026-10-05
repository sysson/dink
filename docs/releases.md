# Release maintenance

## Versioning

Dink, Dinki, and Dinkle share a project release version. Automatic semantic
versioning uses Conventional Commits without forced patch bumps:
`fix:`, `feat:`, and breaking-change markers express release impact. With squash
merging, the squash commit message determines the impact.

Non-release-worthy main pushes run tests but publish no project images or
binaries. The [Tini image](../images/tini/README.md) has an independent lifecycle.

Dinkle and Dinki expose version, commit, source commit date, and development
dirty-worktree status with `--version`. Dink exposes build metadata through its
CLI and Docker version API.

## Checks and publication

The [PR workflow](../.github/workflows/ci.yml) runs Go tests, builds both service
images, and validates release tooling/manifests when related paths change.
Configure branch protection to require its stable `required-checks` job.

The [publishing workflow](../.github/workflows/publish.yml):

1. Tests the main commit and calculates the next version in dry-run mode.
2. Builds both amd64/arm64 service images with matching embedded metadata.
3. Packages six Dinkle platform archives, digest-pinned manifests, image
   references, version/commit records, and checksums.
4. Verifies archive contents, checksums, image pinning, and image-index platform
   coverage.
5. Creates a Git tag and draft release, uploads assets, and downloads and
   re-verifies the bundle.
6. Creates and verifies versioned image tags, publishes the release, then updates
   convenience `latest` image tags.

Images have staging build tags during preparation. GitHub and registry
publication cannot be atomic. The build-date field uses the source commit date
for consistency across components.

## Recovery

If publication fails after creating a draft, choose **Run workflow** for
`CI and Publish` on `main` and provide its `release_tag`.

The retry checks out and tests that tag's commit. Complete draft bundles are
downloaded and reused without rebuilding. Incomplete drafts are rebuilt, but
conflicting existing assets or image tags are never overwritten. Automatic
releases refuse to overtake unfinished project drafts.

If a rebuild conflicts with a partial draft, inspect it before deliberately
removing conflicting draft assets or discarding the failed draft/tag.
Never delete or replace public-release assets.

A retry of a public release verifies the bundle and image tags without mutating
them or moving `latest` backwards. If the final `latest` update fails, the
immutable release remains usable; repair convenience tags separately.

## Validation limits

Local validation covers packaging, checksums, workflow syntax, and mocked
publication/retry failures. The remote GitHub/GHCR integration needs verification
through a real CI publication. The workflow does not provide automatic rollback
or live-cluster installation testing.
