package server

import (
	"context"
	"log/slog"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"
	"github.com/sysson/dink/pkg/server/middleware"
	"github.com/sysson/dink/pkg/server/router"
	"github.com/sysson/dink/pkg/utils/httputils"
)

const (
	versionMatcher = "/v{version:[0-9.]+}"
)

type Server struct {
	middlewares []middleware.Middleware
	logger      *slog.Logger
}

func New(logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		logger: logger,
	}
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

func (s *Server) makeHTTPHandler(route router.Route) http.HandlerFunc {
	return httputils.HTTPHandler(s.withMiddleware((route.Handler())), s.logger)
}

func (s *Server) CreateMux(ctx context.Context, routers ...router.Router) *chi.Mux {
	r := chi.NewRouter()
	for _, apiRouter := range routers {
		for _, route := range apiRouter.Routes() {
			f := s.makeHTTPHandler(route)
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
