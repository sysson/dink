package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/sysson/dink/core/server/router"
	"github.com/sysson/dink/core/translator"
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
			handler := route.Handler()
			f := httpx.ErrorLogger(func(w http.ResponseWriter, r *http.Request) error {
				return dockerAPIError(handler(w, r))
			})
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

func dockerAPIError(err error) error {
	var translatorError *translator.Error
	if !errors.As(err, &translatorError) {
		return err
	}
	var status int
	switch translatorError.Kind() {
	case translator.KindInvalidArgument:
		status = http.StatusBadRequest
	case translator.KindUnauthenticated:
		status = http.StatusUnauthorized
	case translator.KindForbidden:
		status = http.StatusForbidden
	case translator.KindNotFound:
		status = http.StatusNotFound
	case translator.KindConflict:
		status = http.StatusConflict
	case translator.KindUnsupported:
		status = http.StatusNotImplemented
	case translator.KindUnavailable:
		status = http.StatusServiceUnavailable
	default:
		return fmt.Errorf("unknown translator error kind %d: %w", translatorError.Kind(), err)
	}
	return httpx.NewHTTPError(status, translatorError)
}
