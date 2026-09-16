package router

import (
	"context"
	"fmt"
	"net/http"
	"regexp"

	"github.com/moby/moby/client/pkg/versions"
	"github.com/sysson/dink/core/version"
	"github.com/sysson/syskit/httpx"
)

type Router interface {
	Routes() []Route
}

type Route interface {
	Handler() httpx.HTTPErrorFunc
	Method() string
	Path() string
}

type RouteWrapper func(Route) Route

type localRoute struct {
	method  string
	path    string
	handler httpx.HTTPErrorFunc
}

func (r localRoute) Handler() httpx.HTTPErrorFunc {
	return r.handler
}

func (r localRoute) Method() string {
	return r.method
}

func (r localRoute) Path() string {
	return r.path
}

func NewRoute(method, path string, handler httpx.HTTPErrorFunc, opts ...RouteWrapper) Route {
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

func NewGetRoute(path string, handler httpx.HTTPErrorFunc, opts ...RouteWrapper) Route {
	return NewRoute(http.MethodGet, path, handler, opts...)
}

func NewPostRoute(path string, handler httpx.HTTPErrorFunc, opts ...RouteWrapper) Route {
	return NewRoute(http.MethodPost, path, handler, opts...)
}

func NewPutRoute(path string, handler httpx.HTTPErrorFunc, opts ...RouteWrapper) Route {
	return NewRoute(http.MethodPut, path, handler, opts...)
}

func NewDeleteRoute(path string, handler httpx.HTTPErrorFunc, opts ...RouteWrapper) Route {
	return NewRoute(http.MethodDelete, path, handler, opts...)
}

func NewOptionsRoute(path string, handler httpx.HTTPErrorFunc, opts ...RouteWrapper) Route {
	return NewRoute(http.MethodOptions, path, handler, opts...)
}

func NewHeadRoute(path string, handler httpx.HTTPErrorFunc, opts ...RouteWrapper) Route {
	return NewRoute(http.MethodHead, path, handler, opts...)
}

func WithMinAPIVersion(minVersion string) RouteWrapper {
	return func(r Route) Route {
		return localRoute{
			method: r.Method(),
			path:   r.Path(),
			handler: func(w http.ResponseWriter, req *http.Request) error {
				v, ok := httpx.KeyFromContext[version.APIVersion, string](req.Context(), version.APIVersion{})
				if !ok {
					return httpx.BadRequest(fmt.Errorf("API version not specified"))
				}
				if versions.LessThan(v, minVersion) {
					return httpx.BadRequest(fmt.Errorf("API version %s is not supported. Minimum supported version is %s", v, minVersion))
				}
				return r.Handler()(w, req)
			},
		}
	}
}

type CtxKey string

func RegexRoute(pattern string, handler httpx.HTTPErrorFunc) httpx.HTTPErrorFunc {
	// Convert {name:.*} → (?P<name>.*)
	rePattern := regexp.MustCompile(`\{(\w+):(.*?)\}`)
	regex := rePattern.ReplaceAllStringFunc(pattern, func(m string) string {
		parts := rePattern.FindStringSubmatch(m)
		name := parts[1]
		expr := parts[2]
		return fmt.Sprintf("(?P<%s>%s)", name, expr)
	})

	// Anchor the regex to the full path
	full := "^.*/" + regex + "$"
	re := regexp.MustCompile(full)

	return func(w http.ResponseWriter, r *http.Request) error {
		m := re.FindStringSubmatch(r.URL.Path)
		if m == nil {
			_ = httpx.NotFound(fmt.Errorf("%s not found", r.URL.Path)).WriteJSON(w)
			return nil
		}

		// Extract named params safely
		params := map[string]string{}
		for i, name := range re.SubexpNames() {
			if i > 0 && name != "" {
				params[name] = m[i]
			}
		}

		// Inject into context
		ctx := r.Context()
		for k, v := range params {
			ctx = context.WithValue(ctx, CtxKey(k), v)
		}

		return handler(w, r.WithContext(ctx))
	}
}
