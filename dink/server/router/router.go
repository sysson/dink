package router

import (
	"fmt"
	"net/http"

	"github.com/moby/moby/client/pkg/versions"
	"github.com/sysson/dink/dink/pkg/httputils"
	"github.com/sysson/dink/dink/server/middleware"
)

type Router interface {
	Routes() []Route
}

type Route interface {
	Handler() httputils.HTTPFunc
	Method() string
	Path() string
}

type RouteWrapper func(Route) Route

type localRoute struct {
	method  string
	path    string
	handler httputils.HTTPFunc
}

func (r localRoute) Handler() httputils.HTTPFunc {
	return r.handler
}

func (r localRoute) Method() string {
	return r.method
}

func (r localRoute) Path() string {
	return r.path
}

func NewRoute(method, path string, handler httputils.HTTPFunc, opts ...RouteWrapper) Route {
	var r Route = localRoute{
		method:  method,
		path:    path,
		handler: handler,
	}
	for _, opt := range opts {
		r = opt(r)
	}
	return r
}

func NewGetRoute(path string, handler httputils.HTTPFunc, opts ...RouteWrapper) Route {
	return NewRoute(http.MethodGet, path, handler, opts...)
}

func NewPostRoute(path string, handler httputils.HTTPFunc, opts ...RouteWrapper) Route {
	return NewRoute(http.MethodPost, path, handler, opts...)
}

func NewPutRoute(path string, handler httputils.HTTPFunc, opts ...RouteWrapper) Route {
	return NewRoute(http.MethodPut, path, handler, opts...)
}

func NewDeleteRoute(path string, handler httputils.HTTPFunc, opts ...RouteWrapper) Route {
	return NewRoute(http.MethodDelete, path, handler, opts...)
}

func NewOptionsRoute(path string, handler httputils.HTTPFunc, opts ...RouteWrapper) Route {
	return NewRoute(http.MethodOptions, path, handler, opts...)
}

func NewHeadRoute(path string, handler httputils.HTTPFunc, opts ...RouteWrapper) Route {
	return NewRoute(http.MethodHead, path, handler, opts...)
}

func WithMinAPIVersion(minVersion string) RouteWrapper {
	return func(r Route) Route {
		return localRoute{
			method: r.Method(),
			path:   r.Path(),
			handler: func(w http.ResponseWriter, req *http.Request) error {
				v, ok := httputils.KeyFromContext[middleware.APIVersion, string](req.Context(), middleware.APIVersion{})
				if !ok {
					return httputils.BadRequest(fmt.Errorf("API version not specified"))
				}
				if versions.LessThan(v, minVersion) {
					return httputils.BadRequest(fmt.Errorf("API version %s is not supported. Minimum supported version is %s", v, minVersion))
				}
				return r.Handler()(w, req)
			},
		}
	}
}
