package distribution

import (
	"context"

	"github.com/distribution/reference"
	"github.com/docker/distribution"
	"github.com/moby/moby/api/types/registry"
)

type Translator interface {
	GetRepositories(context.Context, reference.Named, *registry.AuthConfig) ([]distribution.Repository, error)
}
