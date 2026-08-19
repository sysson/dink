package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"
	"github.com/sysson/dink/internal/server/httputils"
	"github.com/sysson/dink/internal/server/middleware"
	"github.com/sysson/dink/internal/server/router"
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

func (s *Server) withMiddleware(next http.Handler) http.Handler {
	for _, v := range slices.Backward(s.middlewares) {
		next = v(next)
	}
	return next
}

func (s *Server) makeHTTPHandler(route router.Route) http.HandlerFunc {
	handlerFunc := func(w http.ResponseWriter, r *http.Request) {
		err := route.Handler()(r.Context(), w, r)
		if err == nil {
			return
		}
		if httpErr, ok := errors.AsType[*httputils.HTTPError](err); ok {
			s.logger.ErrorContext(r.Context(), "HTTP handler error", "error", err)
			_ = httpErr.WriteJSON(w)
			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			_ = httputils.RequestTimeout().WriteJSON(w)
			return
		}
		s.logger.ErrorContext(r.Context(), "HTTP server error", "error", err)
		_ = httputils.ServerError().WriteJSON(w)
	}
	handler := s.withMiddleware(http.HandlerFunc(handlerFunc))
	return handler.ServeHTTP
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
		_ = httputils.NotFound().WriteJSON(w)
	})
	r.HandleFunc(versionMatcher+"/*", notFoundHandler)
	r.NotFound(notFoundHandler)
	r.MethodNotAllowed(notFoundHandler)
	return r
}
