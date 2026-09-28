package plugin

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

type Translator interface {
	DisablePlugin(context.Context, string, *backend.PluginDisableConfig) error
	EnablePlugin(context.Context, string, *backend.PluginEnableConfig) error
	ListPlugins(context.Context, filters.Args) ([]plugintypes.Plugin, error)
	InspectPlugin(context.Context, string) (*plugintypes.Plugin, error)
	RemovePlugin(context.Context, string, *backend.PluginRmConfig) error
	SetPlugin(context.Context, string, []string) error
	PluginPrivileges(context.Context, reference.Named, http.Header, *registry.AuthConfig) (plugintypes.Privileges, error)
	PullPlugin(context.Context, reference.Named, string, http.Header, *registry.AuthConfig, plugintypes.Privileges, io.Writer) error
	PushPlugin(context.Context, string, http.Header, *registry.AuthConfig, io.Writer) error
	UpgradePlugin(context.Context, reference.Named, string, http.Header, *registry.AuthConfig, plugintypes.Privileges, io.Writer) error
	CreatePluginFromContext(context.Context, io.ReadCloser, *backend.PluginCreateConfig) error
}
