package translator

import (
	"context"
	"io"
	"net/http"

	"github.com/distribution/reference"
	plugintypes "github.com/moby/moby/api/types/plugin"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/pkg/filters"
)

func (d *Docker) DisablePlugin(context.Context, string, *backend.PluginDisableConfig) error {
	return ErrNotImplemented
}

func (d *Docker) EnablePlugin(context.Context, string, *backend.PluginEnableConfig) error {
	return ErrNotImplemented
}

func (d *Docker) ListPlugins(context.Context, filters.Args) ([]plugintypes.Plugin, error) {
	return nil, ErrNotImplemented
}

func (d *Docker) InspectPlugin(context.Context, string) (*plugintypes.Plugin, error) {
	return nil, ErrNotImplemented
}

func (d *Docker) RemovePlugin(context.Context, string, *backend.PluginRmConfig) error {
	return ErrNotImplemented
}

func (d *Docker) SetPlugin(context.Context, string, []string) error {
	return ErrNotImplemented
}

func (d *Docker) PluginPrivileges(context.Context, reference.Named, http.Header, *registry.AuthConfig) (plugintypes.Privileges, error) {
	return plugintypes.Privileges{}, ErrNotImplemented
}

func (d *Docker) PullPlugin(context.Context, reference.Named, string, http.Header, *registry.AuthConfig, plugintypes.Privileges, io.Writer) error {
	return ErrNotImplemented
}

func (d *Docker) PushPlugin(context.Context, string, http.Header, *registry.AuthConfig, io.Writer) error {
	return ErrNotImplemented
}

func (d *Docker) UpgradePlugin(context.Context, reference.Named, string, http.Header, *registry.AuthConfig, plugintypes.Privileges, io.Writer) error {
	return ErrNotImplemented
}

func (d *Docker) CreatePluginFromContext(context.Context, io.ReadCloser, *backend.PluginCreateConfig) error {
	return ErrNotImplemented
}
