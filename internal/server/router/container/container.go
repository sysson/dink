package container

import "github.com/sysson/dink/internal/server/router"

type containerRouter struct {
	translator Translator
	routes     []router.Route
}

func New(translator Translator) router.Router {
	cr := &containerRouter{
		translator: translator,
		routes:     []router.Route{},
	}
	cr.initRoutes()
	return cr
}

func (cr *containerRouter) Routes() []router.Route {
	return cr.routes
}
func (cr *containerRouter) initRoutes() {
	cr.routes = []router.Route{
		// HEAD
		router.NewHeadRoute("/containers/{name:.*}/archive", cr.headContainersArchive),
		// GET
		router.NewGetRoute("/containers/json", cr.getContainersJSON),
		router.NewGetRoute("/containers/{name:.*}/export", cr.getContainersExport),
		router.NewGetRoute("/containers/{name:.*}/changes", cr.getContainersChanges),
		router.NewGetRoute("/containers/{name:.*}/json", cr.getContainersByName),
		router.NewGetRoute("/containers/{name:.*}/top", cr.getContainersTop),
		router.NewGetRoute("/containers/{name:.*}/logs", cr.getContainersLogs),
		router.NewGetRoute("/containers/{name:.*}/stats", cr.getContainersStats),
		router.NewGetRoute("/containers/{name:.*}/attach/ws", cr.wsContainersAttach),
		router.NewGetRoute("/exec/{id:.*}/json", cr.getExecByID),
		router.NewGetRoute("/containers/{name:.*}/archive", cr.getContainersArchive),
		// POST
		router.NewPostRoute("/containers/create", cr.postContainersCreate),
		router.NewPostRoute("/containers/{name:.*}/kill", cr.postContainersKill),
		router.NewPostRoute("/containers/{name:.*}/pause", cr.postContainersPause),
		router.NewPostRoute("/containers/{name:.*}/unpause", cr.postContainersUnpause),
		router.NewPostRoute("/containers/{name:.*}/restart", cr.postContainersRestart),
		router.NewPostRoute("/containers/{name:.*}/start", cr.postContainersStart),
		router.NewPostRoute("/containers/{name:.*}/stop", cr.postContainersStop),
		router.NewPostRoute("/containers/{name:.*}/wait", cr.postContainersWait),
		router.NewPostRoute("/containers/{name:.*}/resize", cr.postContainersResize),
		router.NewPostRoute("/containers/{name:.*}/attach", cr.postContainersAttach),
		router.NewPostRoute("/containers/{name:.*}/exec", cr.postContainerExecCreate),
		router.NewPostRoute("/exec/{name:.*}/start", cr.postContainerExecStart),
		router.NewPostRoute("/exec/{name:.*}/resize", cr.postContainerExecResize),
		router.NewPostRoute("/containers/{name:.*}/rename", cr.postContainerRename),
		router.NewPostRoute("/containers/{name:.*}/update", cr.postContainerUpdate),
		router.NewPostRoute("/containers/prune", cr.postContainersPrune, router.WithMinAPIVersion("1.25")),
		router.NewPostRoute("/commit", cr.postCommit),
		// PUT
		router.NewPutRoute("/containers/{name:.*}/archive", cr.putContainersArchive),
		// DELETE
		router.NewDeleteRoute("/containers/{name:.*}", cr.deleteContainers),
	}
}
