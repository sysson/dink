package container

import (
	"context"
	"io"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/pkg/filters"
)

type execTranslator interface {
	ContainerExecCreate(context.Context, string, *container.ExecCreateRequest) (string, error)
	ContainerExecInspect(context.Context, string) (*container.ExecInspectResponse, error)
	ContainerExecResize(context.Context, string, uint32, uint32) error
	ContainerExecStart(context.Context, string, backend.ExecStartConfig) error
	ExecExists(context.Context, string) (bool, error)
}

type copyTranslator interface {
	ContainerArchivePath(context.Context, string, string) (io.ReadCloser, *container.PathStat, error)
	ContainerExport(context.Context, string, io.Writer) error
	ContainerExtractToDir(context.Context, string, string, bool, bool, io.Reader) error
	ContainerStatPath(context.Context, string, string) (*container.PathStat, error)
}

type stateTranslator interface {
	ContainerCreate(ctx context.Context, cfg backend.ContainerCreateConfig) (container.CreateResponse, error)
	ContainerKill(context.Context, string, string) error
	ContainerPause(context.Context, string) error
	ContainerRename(context.Context, string, string) error
	ContainerResize(context.Context, string, uint32, uint32) error
	ContainerRestart(context.Context, string, backend.ContainerStopOptions) error
	ContainerRm(context.Context, string, *backend.ContainerRmConfig) error
	ContainerStart(context.Context, string, string, string) error
	ContainerStop(context.Context, string, backend.ContainerStopOptions) error
	ContainerUnpause(context.Context, string) error
	ContainerUpdate(context.Context, string, *container.UpdateConfig) (container.UpdateResponse, error)
	ContainerWait(context.Context, string, container.WaitCondition) (container.WaitResponse, error)
}

type monitorTranslator interface {
	ContainerChanges(context.Context, string) ([]container.FilesystemChange, error)
	ContainerInspect(context.Context, string, backend.ContainerInspectOptions) (*container.InspectResponse, network.HardwareAddr, error)
	ContainerLogs(context.Context, string, *backend.ContainerLogsOptions) (<-chan *backend.LogMessage, bool, error)
	ContainerStats(context.Context, string, *backend.ContainerStatsConfig) error
	ContainerTop(context.Context, string, string) (*container.TopResponse, error)
	Containers(context.Context, *backend.ContainerListOptions) ([]container.Summary, error)
}

type attachTranslator interface {
	ContainerAttach(context.Context, string, *backend.ContainerAttachConfig) error
}

type systemTranslator interface {
	ContainerPrune(context.Context, filters.Args) (*container.PruneReport, error)
}

type commitTranslator interface {
	CreateImageFromContainer(context.Context, string, *backend.CreateImageConfig) (string, error)
}

type Translator interface {
	commitTranslator
	execTranslator
	copyTranslator
	stateTranslator
	monitorTranslator
	attachTranslator
	systemTranslator
}
