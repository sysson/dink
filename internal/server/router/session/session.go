package session

import "github.com/sysson/dink/internal/server/router"

type sessionRouter struct {
	translator Translator
	routes     []router.Route
}

func NewRouter(t Translator) router.Router {
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
