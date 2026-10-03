package buildkit

import (
	"context"
	"errors"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Empty preserves protobuf unknown fields, allowing session services to be
// relayed without depending on their individual request and response types.
func forwardRPC(ctx context.Context, conn *grpc.ClientConn, method string, first *emptypb.Empty, server grpc.ServerStream) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	client, err := conn.NewStream(ctx, &grpc.StreamDesc{ClientStreams: true, ServerStreams: true}, method)
	if err != nil {
		return err
	}
	if first != nil {
		if err := client.SendMsg(first); err != nil {
			return err
		}
	}
	sent := make(chan error, 1)
	go func() {
		for {
			frame := new(emptypb.Empty)
			if err := server.RecvMsg(frame); err != nil {
				if errors.Is(err, io.EOF) {
					sent <- client.CloseSend()
				} else {
					sent <- err
					cancel()
				}
				return
			}
			if err := client.SendMsg(frame); err != nil {
				sent <- err
				cancel()
				return
			}
		}
	}()
	err = relayResponses(client, server)
	select {
	case sendErr := <-sent:
		if err == nil {
			err = sendErr
		}
	default:
	}
	return err
}

func relayResponses(client grpc.ClientStream, server grpc.ServerStream) error {
	headers, err := client.Header()
	if err != nil {
		return err
	}
	if err := server.SendHeader(headers); err != nil {
		return err
	}
	defer func() { server.SetTrailer(client.Trailer()) }()
	for {
		frame := new(emptypb.Empty)
		if err := client.RecvMsg(frame); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := server.SendMsg(frame); err != nil {
			return err
		}
	}
}

func outgoingContext(ctx context.Context) context.Context {
	md, _ := metadata.FromIncomingContext(ctx)
	return metadata.NewOutgoingContext(ctx, md.Copy())
}
