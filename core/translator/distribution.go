package translator

import (
	"context"

	"github.com/distribution/reference"
	"github.com/docker/distribution"
	"github.com/moby/moby/api/types/registry"
)

func (d *Docker) GetRepositories(context.Context, reference.Named, *registry.AuthConfig) ([]distribution.Repository, error) {
	return nil, ErrNotImplemented
}
