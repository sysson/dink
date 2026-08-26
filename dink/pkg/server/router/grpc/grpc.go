package grpc

import (
	"github.com/sysson/dink/dink/pkg/server/router"
	"golang.org/x/net/http2"
	"google.golang.org/grpc"
)

type grpcRouter struct {
	routes     []router.Route
	grpcServer *grpc.Server
	h2Server   *http2.Server
}

func New(translator Translator) *grpcRouter {

	r := &grpcRouter{
		grpcServer: grpc.NewServer(),
		h2Server:   &http2.Server{},
	}
	r.initRoutes()
	return r
}

func (gr *grpcRouter) Routes() []router.Route {
	return gr.routes
}

func (gr *grpcRouter) initRoutes() {
	gr.routes = []router.Route{
		router.NewPostRoute("/grpc", gr.serveGRPC),
	}
}
