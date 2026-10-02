package translator

import "google.golang.org/grpc"

func (b *Builder) GRPCServer() *grpc.Server { return b.grpc }
