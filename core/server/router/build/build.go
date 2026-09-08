package build

import "github.com/sysson/dink/core/server/router"

type buildRouter struct {
	translator Translator
	routes     []router.Route
}

func New(translator Translator) router.Router {
	br := &buildRouter{
		translator: translator,
		routes:     []router.Route{},
	}
	br.initRoutes()
	return br
}

func (br *buildRouter) Routes() []router.Route {
	return br.routes
}
func (br *buildRouter) initRoutes() {
	br.routes = []router.Route{
		router.NewPostRoute("/build", br.postBuild),
		router.NewPostRoute("/build/prune", br.postPrune, router.WithMinAPIVersion("1.31")),
		router.NewPostRoute("/build/cancel", br.postCancel),
	}
}
