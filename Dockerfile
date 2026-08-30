FROM golang:1.27-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/dink ./cmd/dink

# Built from a binary compiled on the host, so the Tilt loop skips the Go
# toolchain layers entirely. Selected with --target=dev.
FROM gcr.io/distroless/static-debian12:nonroot AS dev
COPY .tilt/dink /usr/local/bin/dink
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/dink"]

FROM gcr.io/distroless/static-debian12:nonroot AS final
COPY --from=build /out/dink /usr/local/bin/dink
USER 65532:65532
EXPOSE 2375 2376
ENTRYPOINT ["/usr/local/bin/dink"]
