package grpc

import (
	"net/http"

	"github.com/sysson/dink/core/buildkit"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/syskit/logx"
)

func (gr *grpcRouter) serveGRPC(w http.ResponseWriter, r *http.Request) error {
	conn, err := buildkit.Hijack(w, r)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	id, _ := identity.FromContext(r.Context())
	if err := buildkit.ServeHTTP2(r.Context(), conn,
		http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			gr.grpcServer.ServeHTTP(w, request.WithContext(identity.NewContext(request.Context(), id)))
		}),
	); err != nil {
		// The connection is hijacked, so the HTTP error handler cannot write a response.
		logx.G(r.Context()).Error("serving upgraded gRPC connection", "error", err)
	}
	return nil
}
