package container

import (
	"github.com/sysson/dink/internal/server/router"
)

type containerRouter struct {
	translator Translator
	routes     []router.Route
}

func New(t Translator) router.Router {
	r := &containerRouter{
		translator: t,
	}
	r.initRoutes()
	return r
}

func (c *containerRouter) Routes() []router.Route {
	return c.routes
}

func (c *containerRouter) initRoutes() {
	c.routes = []router.Route{
		router.NewPostRoute("/containers/create", c.postContainersCreate()),
	}
}
