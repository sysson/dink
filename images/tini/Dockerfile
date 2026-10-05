# syntax=docker/dockerfile:1
FROM alpine:3.23@sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0 AS build

RUN apk add --no-cache build-base cmake curl

WORKDIR /src
RUN curl --fail --location --retry 3 \
      https://codeload.github.com/krallin/tini/tar.gz/de40ad007797e0dcd8b7126f27bb87401d224240 \
      --output tini.tar.gz && \
    echo "79daf78f1107b26c0252e979038215d86a937c2dee98232d28a88c61e3e4ff2a  tini.tar.gz" | sha256sum -c - && \
    tar -xzf tini.tar.gz --strip-components=1 && \
    # Tini 0.19.0 omits libgen.h, required for basename with current musl.
    cmake -S . -B build -DCMAKE_POLICY_VERSION_MINIMUM=3.5 \
      "-DCMAKE_C_FLAGS=-include libgen.h" && \
    cmake --build build --target tini-static && \
    mkdir -p /out/licenses && \
    cp build/tini-static /out/tini && \
    chmod 0755 /out/tini && \
    cp LICENSE /out/licenses/tini-LICENSE && \
    curl --fail --location --retry 3 \
      'https://git.musl-libc.org/cgit/musl/plain/COPYRIGHT?h=v1.2.5' \
      --output /out/licenses/musl-COPYRIGHT && \
    echo "f9bc4423732350eb0b3f7ed7e91d530298476f8fec0c6c427a1c04ade22655af  /out/licenses/musl-COPYRIGHT" | sha256sum -c - && \
    /out/tini --version && \
    readelf -l /out/tini > /tmp/program-headers && \
    readelf -d /out/tini > /tmp/dynamic-section && \
    ! grep -q INTERP /tmp/program-headers && \
    ! grep -q NEEDED /tmp/dynamic-section

FROM build AS test
COPY tests/ /tests/
RUN cc -std=c99 -Wall -Wextra -Werror -O2 -static /tests/fixture.c -o /tests/fixture
ENTRYPOINT ["/bin/sh", "/tests/run.sh"]

FROM scratch AS runtime
LABEL org.opencontainers.image.title="dink-init" \
      org.opencontainers.image.description="Statically linked Tini for Dink process supervision." \
      org.opencontainers.image.source="https://github.com/sysson/dink" \
      org.opencontainers.image.version="0.19.0" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/ /
ENTRYPOINT ["/tini", "--"]

FROM runtime AS test-pid1
COPY --from=test /tests/fixture /fixture
CMD ["/fixture", "orphan"]

FROM runtime AS tini
