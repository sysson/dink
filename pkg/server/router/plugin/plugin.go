package plugin

import "github.com/sysson/dink/pkg/server/router"

type pluginRouter struct {
	translator Translator
	routes     []router.Route
}

// NewRouter initializes a new plugin router
func New(t Translator) router.Router {
	r := &pluginRouter{
		translator: t,
	}
	r.initRoutes()
	return r
}

// Routes returns the available routers to the plugin controller
func (pr *pluginRouter) Routes() []router.Route {
	return pr.routes
}

func (pr *pluginRouter) initRoutes() {
	pr.routes = []router.Route{
		router.NewGetRoute("/plugins", pr.listPlugins),
		router.NewGetRoute("/plugins/{name:.*}/json", pr.inspectPlugin),
		router.NewGetRoute("/plugins/privileges", pr.getPrivileges),
		router.NewDeleteRoute("/plugins/{name:.*}", pr.removePlugin),
		router.NewPostRoute("/plugins/{name:.*}/enable", pr.enablePlugin),
		router.NewPostRoute("/plugins/{name:.*}/disable", pr.disablePlugin),
		router.NewPostRoute("/plugins/pull", pr.pullPlugin),
		router.NewPostRoute("/plugins/{name:.*}/push", pr.pushPlugin),
		router.NewPostRoute("/plugins/{name:.*}/upgrade", pr.upgradePlugin, router.WithMinAPIVersion("1.26")),
		router.NewPostRoute("/plugins/{name:.*}/set", pr.setPlugin),
		router.NewPostRoute("/plugins/create", pr.createPlugin),
	}
}
