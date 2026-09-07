package checkpoint

import "github.com/sysson/dink/dink/server/router"

type checkpointRouter struct {
	translator Translator
	routes     []router.Route
}

func New(translator Translator) router.Router {
	cr := &checkpointRouter{
		translator: translator,
		routes:     []router.Route{},
	}
	cr.initRoutes()
	return cr
}

func (cr *checkpointRouter) Routes() []router.Route {
	return cr.routes
}
func (cr *checkpointRouter) initRoutes() {
	cr.routes = []router.Route{
		router.NewGetRoute("/containers/{name:.*}/checkpoints", cr.postCheckpointCreate),
		router.NewPostRoute("/containers/{name:.*}/checkpoints", cr.postCheckpointDelete, router.WithMinAPIVersion("1.31")),
		router.NewDeleteRoute("/containers/{name}/checkpoints/{checkpoint}", cr.postCheckpointList),
	}
}
