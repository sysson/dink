package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/sysson/dink/pkg/config"
	"github.com/sysson/dink/sdk/auth/v1/authconnect"
)

// plugin is a single configured authorization plugin.
type plugin struct {
	name   string
	client authconnect.AuthPluginServiceClient
}

// newPlugin builds a connect client for cfg. cfg.Path is either a unix
// socket path (dialed directly, Docker authz-plugin style) or an http(s)
// base URL.
func newPlugin(cfg config.AuthPlugin) (plugin, error) {
	if cfg.Name == "" || cfg.Path == "" {
		return plugin{}, fmt.Errorf("auth plugin requires both a name and a path")
	}

	httpClient := http.DefaultClient
	baseURL := cfg.Path
	if socket, ok := strings.CutPrefix(cfg.Path, "unix://"); ok {
		baseURL = "http://" + cfg.Name
		httpClient = &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socket)
				},
			},
		}
	}

	return plugin{
		name:   cfg.Name,
		client: authconnect.NewAuthPluginServiceClient(httpClient, baseURL),
	}, nil
}
