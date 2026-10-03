package grpc

import "google.golang.org/grpc"

type Translator interface {
	GRPCServer() *grpc.Server
}
