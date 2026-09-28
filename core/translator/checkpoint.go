package translator

import (
	"context"

	checkpointtypes "github.com/moby/moby/api/types/checkpoint"
	"github.com/moby/moby/v2/daemon/server/backend"
)

func (d *Docker) CheckpointCreate(context.Context, string, checkpointtypes.CreateRequest) error {
	return ErrNotImplemented
}

func (d *Docker) CheckpointDelete(context.Context, string, backend.CheckpointDeleteOptions) error {
	return ErrNotImplemented
}

func (d *Docker) CheckpointList(context.Context, string, backend.CheckpointListOptions) ([]checkpointtypes.Summary, error) {
	return nil, ErrNotImplemented
}
