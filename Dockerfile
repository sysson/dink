FROM golang:1.27-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/dink ./cmd/dink

FROM gcr.io/distroless/static-debian12:nonroot AS final
COPY --from=build /out/dink /usr/local/bin/dink
USER 65532:65532
EXPOSE 2375 2376
ENTRYPOINT ["/usr/local/bin/dink"]
