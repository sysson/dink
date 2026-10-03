FROM --platform=$BUILDPLATFORM golang:1.27-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
COPY sdk/go.mod sdk/go.sum ./sdk/
RUN go mod download

COPY cmd/ ./cmd/
COPY pkg/ ./pkg/
COPY core/ ./core/
COPY sdk/ ./sdk/

FROM build AS build-dink
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/dink ./cmd/dink

FROM build AS build-dinki
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/dinki ./cmd/dinki && \
    mkdir -p /out/dinki-data && \
    chmod 0750 /out/dinki-data && \
    chown 65532:65532 /out/dinki-data

FROM gcr.io/distroless/static-debian12:nonroot AS dink
LABEL org.opencontainers.image.description="Dink translates Docker Engine API requests into Kubernetes workloads."
COPY --from=build-dink /out/dink /usr/local/bin/dink
USER 65532:65532
EXPOSE 2375 2376
ENTRYPOINT ["/usr/local/bin/dink"]

FROM gcr.io/distroless/static-debian12:nonroot AS dinki
LABEL org.opencontainers.image.description="Dinki is the tenant OCI registry used by Dink to store and serve container images."
COPY --from=build-dinki /out/dinki /usr/local/bin/dinki
COPY --from=build-dinki --chown=65532:65532 /out/dinki-data /var/lib/dinki
USER 65532:65532
EXPOSE 5000
ENTRYPOINT ["/usr/local/bin/dinki"]