# Builds one example plugin: --build-arg PLUGIN=auth-plugin, secrets-plugin or volume-plugin.
FROM golang:1.27-bookworm AS build
ARG PLUGIN
WORKDIR /src
COPY go.mod go.sum ./
COPY sdk/go.mod sdk/go.sum ./sdk/
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/plugin ./examples/${PLUGIN}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/plugin /usr/local/bin/plugin
USER 65532:65532
EXPOSE 8443
ENTRYPOINT ["/usr/local/bin/plugin"]
