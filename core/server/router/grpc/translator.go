package grpc

import "google.golang.org/grpc"

type Translator interface {
	RegisterGRPC(*grpc.Server)
}
