package volume

import (
	"context"

	volumetypes "github.com/moby/moby/api/types/volume"
	"github.com/sysson/dink/pkg/filters"
)

type Translator interface {
	ListVolumes(context.Context, filters.Args) ([]volumetypes.Volume, []string, error)
	GetVolume(context.Context, string) (*volumetypes.Volume, error)
	CreateVolume(context.Context, volumetypes.CreateRequest) (*volumetypes.Volume, error)
	RemoveVolume(context.Context, string, bool) error
	PruneVolumes(context.Context, filters.Args) (*volumetypes.PruneReport, error)
}

type ClusterTranslator interface {
	GetVolume(context.Context, string) (volumetypes.Volume, error)
	GetVolumes(context.Context, filters.Args) ([]volumetypes.Volume, error)
	CreateVolume(context.Context, volumetypes.CreateRequest) (*volumetypes.Volume, error)
	RemoveVolume(context.Context, string, bool) error
	UpdateVolume(context.Context, string, uint64, *volumetypes.ClusterVolumeSpec) error
	IsManager(context.Context) (bool, error)
}
