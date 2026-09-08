package grpc

import (
	"net/http"

	"github.com/sysson/dink/core/server/router"
	"google.golang.org/grpc"
)

type grpcRouter struct {
	routes     []router.Route
	grpcServer *grpc.Server
	h2Server   *http.Server
}

func New(translator Translator) *grpcRouter {

	r := &grpcRouter{
		grpcServer: grpc.NewServer(),
		h2Server:   &http.Server{},
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
