package debug

import (
	"expvar"
	"net/http"
	"net/http/pprof"

	"github.com/sysson/dink/pkg/server/router"
	"github.com/sysson/dink/pkg/utils/httputils"
)

func New() router.Router {
	r := &debugRouter{}
	r.initRoutes()
	return r
}

type debugRouter struct {
	routes []router.Route
}

func (r *debugRouter) initRoutes() {
	r.routes = []router.Route{
		router.NewGetRoute("/debug/vars", frameworkAdaptHandler(expvar.Handler())),
		router.NewGetRoute("/debug/pprof/", frameworkAdaptHandlerFunc(pprof.Index)),
		router.NewGetRoute("/debug/pprof/cmdline", frameworkAdaptHandlerFunc(pprof.Cmdline)),
		router.NewGetRoute("/debug/pprof/profile", frameworkAdaptHandlerFunc(pprof.Profile)),
		router.NewGetRoute("/debug/pprof/symbol", frameworkAdaptHandlerFunc(pprof.Symbol)),
		router.NewGetRoute("/debug/pprof/trace", frameworkAdaptHandlerFunc(pprof.Trace)),
		router.NewGetRoute("/debug/pprof/{name}", handlePprof),
	}
}

func (r *debugRouter) Routes() []router.Route {
	return r.routes
}

func frameworkAdaptHandler(handler http.Handler) httputils.HTTPFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		handler.ServeHTTP(w, r)
		return nil
	}
}

func frameworkAdaptHandlerFunc(handler http.HandlerFunc) httputils.HTTPFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		handler(w, r)
		return nil
	}
}
