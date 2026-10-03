package buildkit

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	control "github.com/moby/buildkit/api/services/control"
	"github.com/sysson/dink/core/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type http2Control struct {
	control.UnimplementedControlServer
	identities chan identity.Identity
}

func (s *http2Control) Info(ctx context.Context, _ *control.InfoRequest) (*control.InfoResponse, error) {
	id, _ := identity.FromContext(ctx)
	s.identities <- id
	return &control.InfoResponse{}, nil
}

func TestServeHTTP2ConnectionLifetime(t *testing.T) {
	for _, cancelServer := range []bool{false, true} {
		name := "peer close"
		if cancelServer {
			name = "context cancellation"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			owner := identity.Identity{Namespace: "tenant", CommonName: "client"}
			serverConn, clientConn := net.Pipe()
			defer func() { _ = clientConn.Close() }()
			gs := grpc.NewServer()
			defer gs.Stop()
			service := &http2Control{identities: make(chan identity.Identity, 2)}
			control.RegisterControlServer(gs, service)
			done := make(chan error, 1)
			go func() {
				done <- ServeHTTP2(identity.NewContext(ctx, owner), serverConn, gs)
			}()
			var dialed atomic.Bool
			client, err := grpc.NewClient("passthrough:///test",
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
					if !dialed.CompareAndSwap(false, true) {
						return nil, net.ErrClosed
					}
					return clientConn, nil
				}))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			for range 2 {
				if _, err := control.NewControlClient(client).Info(ctx, &control.InfoRequest{}); err != nil {
					t.Fatal(err)
				}
				if got := <-service.identities; got != owner {
					t.Fatalf("identity = %v, want %v", got, owner)
				}
				select {
				case err := <-done:
					t.Fatalf("server returned with an active connection: %v", err)
				default:
				}
			}
			if cancelServer {
				cancel()
			} else {
				_ = client.Close()
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("server did not release the upgraded connection")
			}
		})
	}
}

func TestServeHTTP2CancelsBeforePreface(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	serverConn, clientConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	gs := grpc.NewServer()
	defer gs.Stop()
	done := make(chan error, 1)
	go func() { done <- ServeHTTP2(ctx, serverConn, gs) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop before receiving a preface")
	}
}
