module github.com/sysson/dink

go 1.26.5

require (
	connectrpc.com/connect v1.20.0
	google.golang.org/protobuf v1.36.11
)

tool (
	connectrpc.com/connect/cmd/protoc-gen-connect-go
	google.golang.org/protobuf/cmd/protoc-gen-go
)
