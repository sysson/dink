package server

import (
	"context"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"
	"github.com/sysson/dink/dink/pkg/httputils"
	"github.com/sysson/dink/dink/server/middleware"
	"github.com/sysson/dink/dink/server/router"
)

const (
	versionMatcher = "/v{version:[0-9.]+}"
)

type Server struct {
	middlewares []middleware.Middleware
}

func New() *Server {
	return &Server{}
}

func (s *Server) Use(m middleware.Middleware) {
	s.middlewares = append(s.middlewares, m)
}

func (s *Server) withMiddleware(next httputils.HTTPFunc) httputils.HTTPFunc {
	for _, v := range slices.Backward(s.middlewares) {
		next = v(next)
	}
	return next
}

func (s *Server) CreateMux(ctx context.Context, routers ...router.Router) *chi.Mux {
	r := chi.NewRouter()
	for _, apiRouter := range routers {
		for _, route := range apiRouter.Routes() {
			f := httputils.HTTPHandler(ctx, s.withMiddleware(route.Handler()))
			r.Method(route.Method(), route.Path(), f)
			r.Method(route.Method(), versionMatcher+route.Path(), f)
		}
	}

	notFoundHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = httputils.NotFound(httputils.ErrHTTPNotFound).WriteJSON(w)
	})
	r.HandleFunc(versionMatcher+"/*", notFoundHandler)
	r.NotFound(notFoundHandler)
	r.MethodNotAllowed(notFoundHandler)
	return r
}
