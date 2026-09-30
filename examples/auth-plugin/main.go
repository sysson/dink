// Command auth-plugin is an example dink auth plugin that denies privileged containers.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"syscall"

	"github.com/sysson/dink/sdk/auth"
	"github.com/sysson/dink/sdk/plugin"
)

var containerCreate = regexp.MustCompile(`^(/v[\d.]+)?/containers/create(\?|$)`)

type noPrivileged struct{}

func (noPrivileged) AuthorizeRequest(_ context.Context, req *auth.Request) (auth.Decision, error) {
	if req.RequestMethod != "POST" || !containerCreate.MatchString(req.RequestURI) {
		return auth.Decision{Allow: true}, nil
	}
	var body struct {
		HostConfig struct {
			Privileged bool
		}
	}
	if err := json.Unmarshal(req.RequestBody, &body); err != nil {
		return auth.Decision{Msg: "unreadable container config"}, nil
	}
	if body.HostConfig.Privileged {
		return auth.Decision{Msg: "privileged containers are not allowed in " + req.Namespace}, nil
	}
	return auth.Decision{Allow: true}, nil
}

func (noPrivileged) AuthorizeResponse(context.Context, *auth.Response) (auth.Decision, error) {
	return auth.Decision{Allow: true}, nil
}

func main() {
	addr := flag.String("addr", plugin.DefaultAddr, "listen address, host:port or unix:///path")
	tlsDir := flag.String("tls-dir", "", "directory holding tls.crt, tls.key and ca.crt; empty serves plaintext")
	healthAddr := flag.String("health-addr", ":8081", "plaintext address for /livez and /readyz")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := []plugin.Option{
		plugin.WithAddr(*addr),
		plugin.WithHealthAddr(*healthAddr),
		auth.Plugin(noPrivileged{}),
	}
	if *tlsDir != "" {
		opts = append(opts, plugin.WithMutualTLS(*tlsDir))
	}
	if err := plugin.Serve(ctx, plugin.Info{Name: "no-privileged", Version: "0.1.0"}, opts...); err != nil {
		slog.Error("plugin stopped", "error", err)
		os.Exit(1)
	}
}
