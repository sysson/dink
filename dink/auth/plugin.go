package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/sysson/dink/sdk/auth/v1/authconnect"
)

type plugin struct {
	name   string
	client authconnect.AuthPluginServiceClient
}

func newPlugin(a AuthPlugin) (plugin, error) {
	if a.Name == "" || a.Path == "" {
		return plugin{}, fmt.Errorf("auth plugin requires both a name and a path")
	}

	httpClient := http.DefaultClient
	baseURL := a.Path
	if socket, ok := strings.CutPrefix(a.Path, "unix://"); ok {
		baseURL = "http://" + a.Name
		httpClient = &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socket)
				},
			},
		}
	}

	return plugin{
		name:   a.Name,
		client: authconnect.NewAuthPluginServiceClient(httpClient, baseURL),
	}, nil
}
