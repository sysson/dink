package distribution

import (
	"context"

	"github.com/moby/moby/api/types/registry"
)

type Translator interface {
	GetDistributionInfo(context.Context, string, *registry.AuthConfig) (registry.DistributionInspect, error)
}
