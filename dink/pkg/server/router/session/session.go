package session

import "github.com/sysson/dink/dink/pkg/server/router"

type sessionRouter struct {
	translator Translator
	routes     []router.Route
}

func New(t Translator) router.Router {
	r := &sessionRouter{
		translator: t,
	}
	r.initRoutes()
	return r
}

func (sr *sessionRouter) Routes() []router.Route {
	return sr.routes
}

func (sr *sessionRouter) initRoutes() {
	sr.routes = []router.Route{
		router.NewPostRoute("/session", sr.startSession),
	}
}
