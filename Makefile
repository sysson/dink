BUF ?= buf
GO  ?= go
GOLANGLINT ?= golangci-lint

.PHONY: all build test generate lint clean fix

all: build

build:
	$(GO) build ./...

test:
	$(GO) test -race ./...

## generate: Re-generate protobuf + gRPC stubs from proto/ into sdk/*/gen/
generate:
	$(BUF) generate

## lint: Run golangci-lint and buf lint
lint:
	$(BUF) lint
	$(GOLANGLINT) run ./...

clean:
	$(GO) clean ./...

fix:
	$(GO) fix ./...
