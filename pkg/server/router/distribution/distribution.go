package distribution

import "github.com/sysson/dink/pkg/server/router"

type distributionRouter struct {
	translator Translator
	routes     []router.Route
}

func New(translator Translator) router.Router {
	r := &distributionRouter{
		translator: translator,
	}
	r.initRoutes()
	return r
}

func (dr *distributionRouter) Routes() []router.Route {
	return dr.routes
}

func (dr *distributionRouter) initRoutes() {
	dr.routes = []router.Route{
		router.NewGetRoute("/distribution/{name:.*}/json", dr.getDistributionInfo, router.WithMinAPIVersion("1.30")),
	}
}
