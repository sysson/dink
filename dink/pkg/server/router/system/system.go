package system

import "github.com/sysson/dink/dink/pkg/server/router"

type systemRouter struct {
	translator Translator
	cluster    ClusterTranslator
	routes     []router.Route
	builder    BuildTranslator
	features   func() map[string]bool
}

func New(t Translator, c ClusterTranslator, builder BuildTranslator, features func() map[string]bool) router.Router {
	r := &systemRouter{
		translator: t,
		cluster:    c,
		builder:    builder,
		features:   features,
	}

	r.routes = []router.Route{
		router.NewOptionsRoute("/{anyroute:.*}", optionsHandler),
		router.NewGetRoute("/_ping", r.pingHandler),
		router.NewHeadRoute("/_ping", r.pingHandler),
		router.NewGetRoute("/events", r.getEvents),
		router.NewGetRoute("/info", r.getInfo),
		router.NewGetRoute("/version", r.getVersion),
		router.NewGetRoute("/system/df", r.getDiskUsage),
		router.NewPostRoute("/auth", r.postAuth),
	}

	return r
}

func (s *systemRouter) Routes() []router.Route {
	return s.routes
}
