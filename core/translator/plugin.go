package translator

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/distribution/reference"
	plugintypes "github.com/moby/moby/api/types/plugin"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/plugins"
	"github.com/sysson/dink/pkg/filters"
)

func (d *Docker) DisablePlugin(context.Context, string, *backend.PluginDisableConfig) error {
	return ErrNotImplemented
}

func (d *Docker) EnablePlugin(context.Context, string, *backend.PluginEnableConfig) error {
	return ErrNotImplemented
}

func (d *Docker) ListPlugins(ctx context.Context, args filters.Args) ([]plugintypes.Plugin, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	if err := args.Validate(map[string]bool{"enabled": true, "capability": true}); err != nil {
		return nil, InvalidArgument(err)
	}
	enabled, err := args.GetBoolOrDefault("enabled", false)
	if err != nil {
		return nil, InvalidArgument(err)
	}

	result := []plugintypes.Plugin{}
	for _, p := range d.plugins.Visible(id.Namespace) {
		plugin := dockerPlugin(p)
		if args.Contains("enabled") && plugin.Enabled != enabled {
			continue
		}
		if args.Contains("capability") && !slices.ContainsFunc(plugin.Config.Interface.Types, func(c plugintypes.CapabilityID) bool {
			return args.ExactMatch("capability", c.Capability)
		}) {
			continue
		}
		result = append(result, plugin)
	}
	return result, nil
}

// InspectPlugin finds a plugin by name or ID, preferring the tenant's own over a cluster plugin of the same name.
func (d *Docker) InspectPlugin(ctx context.Context, name string) (*plugintypes.Plugin, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	name = strings.TrimSuffix(name, ":latest")
	var found *plugintypes.Plugin
	for _, p := range d.plugins.Visible(id.Namespace) {
		plugin := dockerPlugin(p)
		if p.Name == name || (len(name) >= 12 && strings.HasPrefix(plugin.ID, name)) {
			found = &plugin
		}
	}
	if found == nil {
		return nil, NotFound(fmt.Errorf("plugin %q not found", name))
	}
	return found, nil
}

var pluginCapabilities = map[plugins.Type]plugintypes.CapabilityID{
	plugins.TypeAuth:    {Prefix: "docker", Capability: "authz", Version: "1.0"},
	plugins.TypeSecrets: {Prefix: "dink", Capability: "secretprovider", Version: "1.0"},
	plugins.TypeVolumes: {Prefix: "docker", Capability: "volumedriver", Version: "1.0"},
}

// dockerPlugin describes a dink plugin without its endpoint or error details, which belong to the operator.
func dockerPlugin(p *plugins.Plugin) plugintypes.Plugin {
	ready, version, _ := p.Health()
	pluginID := identity.DockerIDFromUID(p.UID)
	if p.UID == "" {
		pluginID = fmt.Sprintf("%x", sha256.Sum256([]byte("static/"+p.Name)))
	}
	scope := "cluster"
	if p.Namespace != "" {
		scope = p.Namespace
	}
	description := fmt.Sprintf("dink plugin (%s)", scope)
	if version != "" {
		description += ", version " + version
	}
	plugin := plugintypes.Plugin{
		ID:      pluginID,
		Name:    p.Name,
		Enabled: ready,
		Config: plugintypes.Config{
			Description: description,
			Interface:   plugintypes.Interface{Types: []plugintypes.CapabilityID{}},
		},
	}
	for _, t := range p.Types {
		plugin.Config.Interface.Types = append(plugin.Config.Interface.Types, pluginCapabilities[t])
	}
	return plugin
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
