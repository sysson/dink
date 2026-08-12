package command

import (
	"github.com/sysson/dink/internal/server/router"
	"github.com/sysson/dink/internal/server/router/build"
	"github.com/sysson/dink/internal/server/router/checkpoint"
	"github.com/sysson/dink/internal/server/router/container"
	"github.com/sysson/dink/internal/server/router/debug"
	"github.com/sysson/dink/internal/server/router/distribution"
	"github.com/sysson/dink/internal/translator"
)

func buildRouters(t *translator.Translator) []router.Router {

	return []router.Router{
		build.New(t),
		checkpoint.New(t),
		container.New(t),
		debug.New(),
		distribution.New(t),
	}
}
