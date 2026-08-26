package volume

import "github.com/sysson/dink/dink/pkg/server/router"

type volumeRouter struct {
	backend Translator
	cluster ClusterTranslator
	routes  []router.Route
}

func New(t Translator, ct ClusterTranslator) router.Router {
	r := &volumeRouter{
		backend: t,
		cluster: ct,
	}
	r.initRoutes()
	return r
}

func (v *volumeRouter) Routes() []router.Route {
	return v.routes
}

func (v *volumeRouter) initRoutes() {
	v.routes = []router.Route{
		// GET
		router.NewGetRoute("/volumes", v.getVolumesList),
		router.NewGetRoute("/volumes/{name:.*}", v.getVolumeByName),
		// POST
		router.NewPostRoute("/volumes/create", v.postVolumesCreate),
		router.NewPostRoute("/volumes/prune", v.postVolumesPrune, router.WithMinAPIVersion("1.25")),
		// PUT
		router.NewPutRoute("/volumes/{name:.*}", v.putVolumesUpdate, router.WithMinAPIVersion("1.42")),
		// DELETE
		router.NewDeleteRoute("/volumes/{name:.*}", v.deleteVolumes),
	}
}
