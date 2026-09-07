package image

import "github.com/sysson/dink/dink/server/router"

type imageRouter struct {
	translator Translator
	searcher   Searcher
	routes     []router.Route
}

func New(translator Translator, searcher Searcher) router.Router {
	ir := &imageRouter{
		translator: translator,
		searcher:   searcher,
		routes:     []router.Route{},
	}
	ir.initRoutes()
	return ir
}

func (ir *imageRouter) Routes() []router.Route {
	return ir.routes
}
func (ir *imageRouter) initRoutes() {
	ir.routes = []router.Route{
		// GET
		router.NewGetRoute("/images/json", ir.getImagesJSON),
		router.NewGetRoute("/images/search", ir.getImagesSearch),
		router.NewGetRoute("/images/get", ir.getImagesGet),
		router.NewGetRoute("/images/{name:.*}/get", ir.getImagesGet),
		router.NewGetRoute("/images/{name:.*}/history", ir.getImagesHistory),
		router.NewGetRoute("/images/{name:.*}/json", ir.getImagesByName),
		router.NewGetRoute("/images/{name:.*}/attestations", ir.getImageAttestations, router.WithMinAPIVersion("1.55")),
		// POST
		router.NewPostRoute("/images/load", ir.postImagesLoad),
		router.NewPostRoute("/images/create", ir.postImagesCreate),
		router.NewPostRoute("/images/{name:.*}/push", ir.postImagesPush),
		router.NewPostRoute("/images/{name:.*}/tag", ir.postImagesTag),
		router.NewPostRoute("/images/prune", ir.postImagesPrune, router.WithMinAPIVersion("1.25")),
		// DELETE
		router.NewDeleteRoute("/images/{name:.*}", ir.deleteImages),
	}
}
