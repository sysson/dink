package command

import (
	"context"
	"net/http"
	"strings"
)

type httpHandler struct {
	ctx        context.Context
	apiServer  http.Handler
	grpcServer http.Handler
}

func newHTTPHandler(ctx context.Context, apiServer http.Handler, grpcServer http.Handler) *httpHandler {
	return &httpHandler{
		ctx:        ctx,
		apiServer:  apiServer,
		grpcServer: grpcServer,
	}
}

func (h *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
		h.grpcServer.ServeHTTP(w, r)
	} else {
		h.apiServer.ServeHTTP(w, r)
	}
}
