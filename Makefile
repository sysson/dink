PROTOC ?= protoc
GO  ?= go

.PHONY: all build test generate lint clean

all: build

build:
	$(GO) build ./...

test:
	$(GO) test -race ./...

## generate: Re-generate protobuf + gRPC stubs from proto/ into sdk/*/gen/
generate:
	$(PROTOC) --proto_path=./sdk/proto --go_out=./sdk --go_opt=paths=source_relative --go-grpc_out=./sdk --go-grpc_opt=paths=source_relative $(shell find ./sdk/proto -name "*.proto")

## lint: Run golangci-lint and buf lint
lint:
	golangci-lint run ./...

clean:
	$(GO) clean ./...

fix:
	$(GO) fix ./...
