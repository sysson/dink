# Dink init image

This independently published image contains statically linked upstream Tini
v0.19.0 at `/tini`, plus Tini and musl license notices under `/licenses`. The
final image uses `scratch`: it contains no shell, installer, or package manager.
It supports Linux amd64 and arm64, independent of the application's distribution
or libc. Other architectures and Windows containers are not supported.

This is image packaging only. Dink does not yet inject this image or implement
Docker/Compose `--init` with it. Executing `/tini` through a Kubernetes image
volume must be validated against the supported Kubernetes/container runtimes
before integrating that feature.

## Build and test

Run against a real Docker daemon, not Dink's Docker-compatible endpoint:

```sh
docker buildx build --load --target test -t dink-init-test images/tini
docker run --rm --network none dink-init-test
docker buildx build --load --target tini -t dink-init images/tini
docker run --rm --network none --entrypoint /tini dink-init --version
docker buildx build --load --target test-pid1 -t dink-init-pid1-test images/tini
docker run --rm --network none dink-init-pid1-test
```

The test image checks child exit-code propagation, SIGTERM forwarding, and
orphan reaping. Tests use Tini's subreaper option because the test script occupies
PID 1; the production entrypoint is `["/tini", "--"]`, with no process-group
forwarding or exit-code remapping enabled. The scratch-image smoke test also
checks that the binary runs without libraries supplied by a base image.
The `test-pid1` target adds only a static fixture to the production image and
checks orphan reaping with the unmodified entrypoint running as PID 1. CI also
checks exit-code propagation through that entrypoint.

## Publishing and consuming

The [Tini workflow](../../.github/workflows/tini.yml) tests both platforms on
native runners. After successful tests, pushes to `main` affecting this directory
or the workflow publish `ghcr.io/sysson/dink-init`. A manual workflow run on
`main` can rebuild it without a Dink release. Pull requests only build and test.
No in-cluster BuildKit or tenant-registry seeding is required.

Publication produces `sha-<full-git-commit>` and `latest` tags. These are discovery
tags, not immutable references: rebuilding the same source can update packages
in the builder and change the output digest. The workflow summary records the
multi-platform index reference:

```text
ghcr.io/sysson/dink-init@sha256:<index-digest>
```

Pin that digest when consuming the image, including from an operator-managed
mirror. Preserve the complete index and both platform images when mirroring.
Keep older published digests available while workloads reference them.

After the first publication, a repository/package administrator must ensure
the GHCR package is public for anonymous node pulls. Otherwise, provide an
appropriate image-pull Secret in each workload's namespace.

## Updating

The builder image is pinned by its multi-platform digest. Upstream Tini is
pinned by commit and its source archive is checksum-verified. To update Tini,
change the commit URL and archive checksum in the [Dockerfile](Dockerfile),
the version label and test expectation, and this document together.

The build explicitly includes `libgen.h` to supply the `basename` declaration
missing in Tini 0.19.0 when using current musl headers, and sets CMake's minimum
policy version for compatibility with CMake 4. Compiler warnings remain errors.
The musl 1.2.5 copyright notice is downloaded from upstream and checksum-verified;
update that notice and checksum if changing to a builder with another musl version.

Rebuild deliberately for upstream fixes, builder/toolchain or musl updates,
and additional platforms; an old upstream release does not make its linked
dependencies permanently maintenance-free. Alpine package versions are resolved
at build time, so source pins do not imply bit-for-bit reproducible builds.
Review the published provenance/SBOM and adopt the new digest explicitly.
