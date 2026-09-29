package checkpoint

import (
	"context"

	checkpointtypes "github.com/moby/moby/api/types/checkpoint"
	"github.com/moby/moby/v2/daemon/server/backend"
)

type Translator interface {
	CheckpointCreate(context.Context, string, checkpointtypes.CreateRequest) error
	CheckpointDelete(context.Context, string, backend.CheckpointDeleteOptions) error
	CheckpointList(context.Context, string, backend.CheckpointListOptions) ([]checkpointtypes.Summary, error)
}
