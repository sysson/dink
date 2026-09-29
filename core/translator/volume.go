package translator

import (
	"context"

	volumetypes "github.com/moby/moby/api/types/volume"
	"github.com/sysson/dink/pkg/filters"
)

func (d *Docker) ListVolumes(context.Context, filters.Args) ([]volumetypes.Volume, []string, error) {
	return nil, nil, ErrNotImplemented
}

func (d *Docker) GetVolume(context.Context, string) (*volumetypes.Volume, error) {
	return nil, ErrNotImplemented
}

func (d *Docker) CreateVolume(context.Context, volumetypes.CreateRequest) (*volumetypes.Volume, error) {
	return nil, ErrNotImplemented
}

func (d *Docker) RemoveVolume(context.Context, string, bool) error {
	return ErrNotImplemented
}

func (d *Docker) PruneVolumes(context.Context, filters.Args) (*volumetypes.PruneReport, error) {
	return nil, ErrNotImplemented
}

func (s *Swarm) GetVolume(context.Context, string) (volumetypes.Volume, error) {
	return volumetypes.Volume{}, ErrNotImplemented
}

func (s *Swarm) GetVolumes(context.Context, filters.Args) ([]volumetypes.Volume, error) {
	return nil, ErrNotImplemented
}

func (s *Swarm) CreateVolume(context.Context, volumetypes.CreateRequest) (*volumetypes.Volume, error) {
	return nil, ErrNotImplemented
}

func (s *Swarm) RemoveVolume(context.Context, string, bool) error {
	return ErrNotImplemented
}

func (s *Swarm) UpdateVolume(context.Context, string, uint64, *volumetypes.ClusterVolumeSpec) error {
	return ErrNotImplemented
}

func (s *Swarm) IsManager(context.Context) (bool, error) {
	return false, ErrNotImplemented
}
