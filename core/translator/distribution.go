package translator

import (
	"context"

	"github.com/moby/moby/api/types/registry"
	internalregistry "github.com/sysson/dink/core/registry"
)

func (d *Docker) GetDistributionInfo(ctx context.Context, name string, auth *registry.AuthConfig) (registry.DistributionInspect, error) {
	return internalregistry.InspectDistribution(ctx, name, auth)
}
