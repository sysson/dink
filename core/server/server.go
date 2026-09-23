package server

import (
	"context"
	"fmt"
	"net/http"

	"github.com/sysson/dink/core/server/router"
	"github.com/sysson/syskit/httpx"
	"github.com/sysson/syskit/remux"
)

const (
	versionMatcher = "/v{version:[0-9.]+}"
)

type Server struct {
	middlewares []func(http.Handler) http.Handler
}

func New() *Server {
	return &Server{}
}

func (s *Server) Use(m func(http.Handler) http.Handler) {
	s.middlewares = append(s.middlewares, m)
}

func (s *Server) CreateMux(ctx context.Context, routers ...router.Router) http.Handler {
	router := remux.New()
	for _, apiRouter := range routers {
		for _, route := range apiRouter.Routes() {
			f := httpx.ErrorLogger(route.Handler())
			router.Method(route.Method(), route.Path(), f)
		}
	}

	notFoundHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.NotFound(fmt.Errorf("%s not found", r.URL.Path)).WriteJSON(w)
	})

	router.NotFound(notFoundHandler)

	r := remux.New()
	r.Prefix(versionMatcher, router)
	r.Prefix("/", router)

	r.Use(
		s.middlewares...,
	)
	return r
}
