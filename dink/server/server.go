package server

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"
	"github.com/sysson/dink/dink/server/middleware"
	"github.com/sysson/dink/dink/server/router"
	"github.com/sysson/syskit/httpx"
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

func (s *Server) withMiddleware(next http.Handler) http.Handler {
	for _, v := range slices.Backward(s.middlewares) {
		next = v(next)
	}
	return next
}

func (s *Server) CreateMux(ctx context.Context, routers ...router.Router) *chi.Mux {
	r := chi.NewRouter()
	for _, apiRouter := range routers {
		for _, route := range apiRouter.Routes() {
			f := s.withMiddleware(httpx.ErrorLogger(middleware.ErrorLogger(route.Handler())))
			r.Method(route.Method(), route.Path(), f)
			r.Method(route.Method(), versionMatcher+route.Path(), f)
		}
	}

	notFoundHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.NotFound(fmt.Errorf("%s not found", r.URL.Path)).WriteJSON(w)
	})
	r.HandleFunc(versionMatcher+"/*", notFoundHandler)
	r.NotFound(notFoundHandler)
	r.MethodNotAllowed(notFoundHandler)
	return r
}
