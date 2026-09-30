// Package plugins discovers dink plugins and hands out clients for them.
//
// Plugins are registered with Plugin resources. Registrations in the system
// namespace apply to every tenant; registrations in a tenant namespace apply to
// that tenant only.
package plugins

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"k8s.io/apimachinery/pkg/types"

	"github.com/sysson/dink/sdk/auth/v1/authconnect"
	sdkplugin "github.com/sysson/dink/sdk/plugin"
	pluginv1 "github.com/sysson/dink/sdk/plugin/v1"
	"github.com/sysson/dink/sdk/plugin/v1/pluginconnect"
	"github.com/sysson/dink/sdk/secrets/v1/secretsconnect"
	"github.com/sysson/dink/sdk/volumes/v1/volumesconnect"
)

type Type string

const (
	TypeAuth    Type = "auth"
	TypeSecrets Type = "secrets"
	TypeVolumes Type = "volumes"
)

var protoTypes = map[Type]pluginv1.PluginType{
	TypeAuth:    pluginv1.PluginType_PLUGIN_TYPE_AUTH,
	TypeSecrets: pluginv1.PluginType_PLUGIN_TYPE_SECRETS,
	TypeVolumes: pluginv1.PluginType_PLUGIN_TYPE_VOLUMES,
}

// ReservedNames are provider and driver names dink implements itself.
var ReservedNames = []string{"k8s", "local"}

const (
	defaultTimeout = 5 * time.Second
	// retryAfter stops an unreachable plugin from costing a full timeout on every request.
	retryAfter = 10 * time.Second
)

var ErrNotFound = errors.New("plugin not found")

// Static is a plugin configured in dink's own config rather than discovered.
type Static struct {
	Name  string
	Types []Type
	// Endpoint is an http(s) URL or unix:///path/to.sock.
	Endpoint string
}

type Plugin struct {
	Name      string
	Namespace string
	UID       types.UID
	Types     []Type

	generation int64
	httpClient *http.Client
	baseURL    string
	// err is set when the registration is invalid, so lookups fail closed.
	err error
	// changed is called after the plugin's health changes.
	changed func(*Plugin)

	mu        sync.Mutex
	ready     bool
	version   string
	lastErr   error
	checkedAt time.Time
}

func (p *Plugin) Auth() authconnect.AuthPluginServiceClient {
	return authconnect.NewAuthPluginServiceClient(p.httpClient, p.baseURL)
}

func (p *Plugin) Secrets() secretsconnect.SecretsPluginServiceClient {
	return secretsconnect.NewSecretsPluginServiceClient(p.httpClient, p.baseURL)
}

func (p *Plugin) Volumes() volumesconnect.VolumePluginServiceClient {
	return volumesconnect.NewVolumePluginServiceClient(p.httpClient, p.baseURL)
}

func (p *Plugin) String() string {
	if p.Namespace == "" {
		return p.Name
	}
	return p.Namespace + "/" + p.Name
}

// Health reports whether the last handshake succeeded, the plugin's version and the last error.
func (p *Plugin) Health() (ready bool, version string, err error) {
	if p.err != nil {
		return false, "", p.err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ready, p.version, p.lastErr
}

// Observe records the outcome of a call, so an unreachable plugin is handshaken again before its next use.
func (p *Plugin) Observe(err error) {
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded:
	default:
		return
	}
	p.mu.Lock()
	wasReady := p.ready
	p.ready, p.lastErr, p.checkedAt = false, err, time.Now()
	p.mu.Unlock()
	if wasReady && p.changed != nil {
		p.changed(p)
	}
}

// ensureReady handshakes with the plugin until it succeeds, retrying failures no more than every retryAfter.
func (p *Plugin) ensureReady(ctx context.Context) error {
	if p.err != nil {
		return fmt.Errorf("plugin %s: %w", p, p.err)
	}
	p.mu.Lock()
	if p.ready {
		p.mu.Unlock()
		return nil
	}
	if p.lastErr != nil && time.Since(p.checkedAt) < retryAfter {
		err := p.lastErr
		p.mu.Unlock()
		return err
	}
	version, err := p.handshake(ctx)
	changed := p.checkedAt.IsZero() || p.ready != (err == nil) || errText(p.lastErr) != errText(err)
	p.ready, p.version, p.lastErr, p.checkedAt = err == nil, version, err, time.Now()
	p.mu.Unlock()

	if changed && p.changed != nil {
		p.changed(p)
	}
	return err
}

func (p *Plugin) handshake(ctx context.Context) (string, error) {
	info, err := pluginconnect.NewPluginServiceClient(p.httpClient, p.baseURL).Info(ctx, &pluginv1.InfoRequest{})
	if err != nil {
		return "", fmt.Errorf("plugin %s is unavailable: %w", p, err)
	}
	if info.GetApiVersion() != sdkplugin.APIVersion {
		return "", fmt.Errorf("plugin %s uses API version %d, dink supports %d", p, info.GetApiVersion(), sdkplugin.APIVersion)
	}
	for _, t := range p.Types {
		if !slices.Contains(info.GetTypes(), protoTypes[t]) {
			return "", fmt.Errorf("plugin %s is registered as %s but does not implement it", p, t)
		}
	}
	return info.GetVersion(), nil
}

func (p *Plugin) has(t Type) bool {
	// An invalid registration matches every type so lookups hit its error instead of skipping it.
	return p.err != nil || slices.Contains(p.Types, t)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func newPlugin(name, namespace string, types []Type, endpoint string, timeout time.Duration, tlsConfig *tls.Config) (*Plugin, error) {
	if name == "" {
		return nil, errors.New("name must not be empty")
	}
	if slices.Contains(ReservedNames, name) {
		return nil, fmt.Errorf("name %q is reserved", name)
	}
	if len(types) == 0 {
		return nil, errors.New("at least one type is required")
	}
	for _, t := range types {
		if _, ok := protoTypes[t]; !ok {
			return nil, fmt.Errorf("unknown plugin type %q", t)
		}
	}

	transport := &http.Transport{ForceAttemptHTTP2: true, TLSHandshakeTimeout: timeout}
	baseURL := endpoint
	switch {
	case strings.HasPrefix(endpoint, "unix://"):
		socket := strings.TrimPrefix(endpoint, "unix://")
		baseURL = "http://" + name
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}
	case strings.HasPrefix(endpoint, "https://"):
		if tlsConfig == nil {
			return nil, errors.New("dink has no TLS client certificate to call https plugins with")
		}
		transport.TLSClientConfig = tlsConfig.Clone()
	case strings.HasPrefix(endpoint, "http://"):
	default:
		return nil, fmt.Errorf("endpoint %q must be an http(s) or unix:// URL", endpoint)
	}

	return &Plugin{
		Name:       name,
		Namespace:  namespace,
		Types:      slices.Clone(types),
		httpClient: &http.Client{Timeout: timeout, Transport: transport},
		baseURL:    strings.TrimSuffix(baseURL, "/"),
	}, nil
}
