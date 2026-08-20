package dink

import (
	"github.com/sysson/dink/pkg/server/router"
	"github.com/sysson/dink/pkg/server/router/build"
	"github.com/sysson/dink/pkg/server/router/checkpoint"
	"github.com/sysson/dink/pkg/server/router/container"
	"github.com/sysson/dink/pkg/server/router/debug"
	"github.com/sysson/dink/pkg/server/router/distribution"
	"github.com/sysson/dink/pkg/server/router/grpc"
	"github.com/sysson/dink/pkg/translator"
)

func buildRouters(t *translator.Translator) []router.Router {

	return []router.Router{
		build.New(t),
		checkpoint.New(t),
		container.New(t),
		debug.New(),
		distribution.New(t),
		grpc.New(t),
	}
}
