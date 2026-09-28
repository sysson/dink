package swarm

import (
	"context"

	swarmtypes "github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/moby/moby/v2/daemon/server/swarmbackend"
)

type Translator interface {
	Init(context.Context, swarmtypes.InitRequest) (string, error)
	Join(context.Context, swarmtypes.JoinRequest) error
	Leave(context.Context, bool) error
	Inspect(context.Context) (swarmtypes.Swarm, error)
	Update(context.Context, uint64, swarmtypes.Spec, swarmbackend.UpdateFlags) error
	GetUnlockKey(context.Context) (string, error)
	UnlockSwarm(context.Context, swarmtypes.UnlockRequest) error
	GetServices(context.Context, swarmbackend.ServiceListOptions) ([]swarmtypes.Service, error)
	GetService(context.Context, string, bool) (swarmtypes.Service, error)
	CreateService(context.Context, swarmtypes.ServiceSpec, string, bool) (*swarmtypes.ServiceCreateResponse, error)
	UpdateService(context.Context, string, uint64, swarmtypes.ServiceSpec, swarmbackend.ServiceUpdateOptions, bool) (*swarmtypes.ServiceUpdateResponse, error)
	RemoveService(context.Context, string) error
	ServiceLogs(context.Context, *backend.LogSelector, *backend.ContainerLogsOptions) (<-chan *backend.LogMessage, error)
	GetNodes(context.Context, swarmbackend.NodeListOptions) ([]swarmtypes.Node, error)
	GetNode(context.Context, string) (swarmtypes.Node, error)
	UpdateNode(context.Context, string, uint64, swarmtypes.NodeSpec) error
	RemoveNode(context.Context, string, bool) error
	GetTasks(context.Context, swarmbackend.TaskListOptions) ([]swarmtypes.Task, error)
	GetTask(context.Context, string) (swarmtypes.Task, error)
	GetSecrets(context.Context, swarmbackend.SecretListOptions) ([]swarmtypes.Secret, error)
	CreateSecret(context.Context, swarmtypes.SecretSpec) (string, error)
	RemoveSecret(context.Context, string) error
	GetSecret(context.Context, string) (swarmtypes.Secret, error)
	UpdateSecret(context.Context, string, uint64, swarmtypes.SecretSpec) error
	GetConfigs(context.Context, swarmbackend.ConfigListOptions) ([]swarmtypes.Config, error)
	CreateConfig(context.Context, swarmtypes.ConfigSpec) (string, error)
	RemoveConfig(context.Context, string) error
	GetConfig(context.Context, string) (swarmtypes.Config, error)
	UpdateConfig(context.Context, string, uint64, swarmtypes.ConfigSpec) error
}
