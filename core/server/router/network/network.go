package network

import "github.com/sysson/dink/core/server/router"

// networkRouter is a router to talk with the network controller
type networkRouter struct {
	translator Translator
	cluster    ClusterTranslator
	routes     []router.Route
}

// NewRouter initializes a new network router
func New(t Translator, c ClusterTranslator) router.Router {
	r := &networkRouter{
		translator: t,
		cluster:    c,
	}
	r.initRoutes()
	return r
}

// Routes returns the available routes to the network controller
func (n *networkRouter) Routes() []router.Route {
	return n.routes
}

func (n *networkRouter) initRoutes() {
	n.routes = []router.Route{
		// GET
		router.NewGetRoute("/networks", n.getNetworksList),
		router.NewGetRoute("/networks/", n.getNetworksList),
		router.NewGetRoute("/networks/{id:.+}", n.getNetwork),
		// POST
		router.NewPostRoute("/networks/create", n.postNetworkCreate),
		router.NewPostRoute("/networks/{id:.*}/connect", n.postNetworkConnect),
		router.NewPostRoute("/networks/{id:.*}/disconnect", n.postNetworkDisconnect),
		router.NewPostRoute("/networks/prune", n.postNetworkPrune, router.WithMinAPIVersion("1.25")),
		// DELETE
		router.NewDeleteRoute("/networks/{id:.*}", n.deleteNetwork),
	}
}
