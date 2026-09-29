package translator

import (
	"context"

	swarmtypes "github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/moby/moby/v2/daemon/server/swarmbackend"
)

func (s *Swarm) Init(context.Context, swarmtypes.InitRequest) (string, error) {
	return "", ErrNotImplemented
}

func (s *Swarm) Join(context.Context, swarmtypes.JoinRequest) error {
	return ErrNotImplemented
}

func (s *Swarm) Leave(context.Context, bool) error {
	return ErrNotImplemented
}

func (s *Swarm) Inspect(context.Context) (swarmtypes.Swarm, error) {
	return swarmtypes.Swarm{}, ErrNotImplemented
}

func (s *Swarm) Update(context.Context, uint64, swarmtypes.Spec, swarmbackend.UpdateFlags) error {
	return ErrNotImplemented
}

func (s *Swarm) GetUnlockKey(context.Context) (string, error) {
	return "", ErrNotImplemented
}

func (s *Swarm) UnlockSwarm(context.Context, swarmtypes.UnlockRequest) error {
	return ErrNotImplemented
}

func (s *Swarm) GetServices(context.Context, swarmbackend.ServiceListOptions) ([]swarmtypes.Service, error) {
	return nil, ErrNotImplemented
}

func (s *Swarm) GetService(context.Context, string, bool) (swarmtypes.Service, error) {
	return swarmtypes.Service{}, ErrNotImplemented
}

func (s *Swarm) CreateService(context.Context, swarmtypes.ServiceSpec, string, bool) (*swarmtypes.ServiceCreateResponse, error) {
	return nil, ErrNotImplemented
}

func (s *Swarm) UpdateService(context.Context, string, uint64, swarmtypes.ServiceSpec, swarmbackend.ServiceUpdateOptions, bool) (*swarmtypes.ServiceUpdateResponse, error) {
	return nil, ErrNotImplemented
}

func (s *Swarm) RemoveService(context.Context, string) error {
	return ErrNotImplemented
}

func (s *Swarm) ServiceLogs(context.Context, *backend.LogSelector, *backend.ContainerLogsOptions) (<-chan *backend.LogMessage, error) {
	return nil, ErrNotImplemented
}

func (s *Swarm) GetNodes(context.Context, swarmbackend.NodeListOptions) ([]swarmtypes.Node, error) {
	return nil, ErrNotImplemented
}

func (s *Swarm) GetNode(context.Context, string) (swarmtypes.Node, error) {
	return swarmtypes.Node{}, ErrNotImplemented
}

func (s *Swarm) UpdateNode(context.Context, string, uint64, swarmtypes.NodeSpec) error {
	return ErrNotImplemented
}

func (s *Swarm) RemoveNode(context.Context, string, bool) error {
	return ErrNotImplemented
}

func (s *Swarm) GetTasks(context.Context, swarmbackend.TaskListOptions) ([]swarmtypes.Task, error) {
	return nil, ErrNotImplemented
}

func (s *Swarm) GetTask(context.Context, string) (swarmtypes.Task, error) {
	return swarmtypes.Task{}, ErrNotImplemented
}

func (s *Swarm) GetSecrets(context.Context, swarmbackend.SecretListOptions) ([]swarmtypes.Secret, error) {
	return nil, ErrNotImplemented
}

func (s *Swarm) CreateSecret(context.Context, swarmtypes.SecretSpec) (string, error) {
	return "", ErrNotImplemented
}

func (s *Swarm) RemoveSecret(context.Context, string) error {
	return ErrNotImplemented
}

func (s *Swarm) GetSecret(context.Context, string) (swarmtypes.Secret, error) {
	return swarmtypes.Secret{}, ErrNotImplemented
}

func (s *Swarm) UpdateSecret(context.Context, string, uint64, swarmtypes.SecretSpec) error {
	return ErrNotImplemented
}

func (s *Swarm) GetConfigs(context.Context, swarmbackend.ConfigListOptions) ([]swarmtypes.Config, error) {
	return nil, ErrNotImplemented
}

func (s *Swarm) CreateConfig(context.Context, swarmtypes.ConfigSpec) (string, error) {
	return "", ErrNotImplemented
}

func (s *Swarm) RemoveConfig(context.Context, string) error {
	return ErrNotImplemented
}

func (s *Swarm) GetConfig(context.Context, string) (swarmtypes.Config, error) {
	return swarmtypes.Config{}, ErrNotImplemented
}

func (s *Swarm) UpdateConfig(context.Context, string, uint64, swarmtypes.ConfigSpec) error {
	return ErrNotImplemented
}
