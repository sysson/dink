package command

import (
	"github.com/sysson/dink/core/server/router"
	"github.com/sysson/dink/core/server/router/build"
	"github.com/sysson/dink/core/server/router/checkpoint"
	"github.com/sysson/dink/core/server/router/container"
	"github.com/sysson/dink/core/server/router/debug"
	"github.com/sysson/dink/core/server/router/distribution"
	"github.com/sysson/dink/core/server/router/grpc"
	"github.com/sysson/dink/core/server/router/image"
	"github.com/sysson/dink/core/server/router/network"
	"github.com/sysson/dink/core/server/router/plugin"
	"github.com/sysson/dink/core/server/router/session"
	"github.com/sysson/dink/core/server/router/swarm"
	"github.com/sysson/dink/core/server/router/system"
	"github.com/sysson/dink/core/server/router/volume"
	"github.com/sysson/dink/core/translator"
)

func buildRouters(t *translator.Translator) []router.Router {

	return []router.Router{
		build.New(t.Builder()),
		checkpoint.New(t.Docker()),
		container.New(t.Docker()),
		debug.New(),
		distribution.New(t.Docker()),
		grpc.New(t.Builder()),
		image.New(t.Registry(), t.Registry()),
		network.New(t.Docker(), t.Swarm()),
		plugin.New(t.Docker()),
		session.New(t.Docker()),
		swarm.New(t.Swarm()),
		system.New(t.Docker(), t.Swarm(), t.Builder(), func() map[string]bool { return map[string]bool{} }),
		volume.New(t.Docker(), t.Swarm()),
	}
}
