// Command volume-plugin is an example dink volume driver that maps a "tier"
// option onto storage classes, e.g.
//
//	docker volume create --driver tiers -o tier=fast -o size=20Gi db
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/sysson/dink/sdk/plugin"
	"github.com/sysson/dink/sdk/volumes"
)

type tiers map[string]string

func (t tiers) Create(_ context.Context, req *volumes.Request) (volumes.Claim, error) {
	for key := range req.Options {
		if key != "tier" && key != "size" {
			return volumes.Claim{}, fmt.Errorf("unknown option %q", key)
		}
	}
	tier := req.Options["tier"]
	if tier == "" {
		tier = "standard"
	}
	class, ok := t[tier]
	if !ok {
		return volumes.Claim{}, fmt.Errorf("unknown tier %q", tier)
	}
	return volumes.Claim{StorageClassName: class, Size: req.Options["size"]}, nil
}

// Remove has nothing to release: the storage class's provisioner owns the backing volume.
func (tiers) Remove(context.Context, *volumes.Request) error {
	return nil
}

func main() {
	addr := flag.String("addr", plugin.DefaultAddr, "listen address, host:port or unix:///path")
	tlsDir := flag.String("tls-dir", "", "directory holding tls.crt, tls.key and ca.crt; empty serves plaintext")
	healthAddr := flag.String("health-addr", ":8081", "plaintext address for /livez and /readyz")
	fast := flag.String("fast-class", "fast-ssd", "storage class for tier=fast")
	standard := flag.String("standard-class", "standard", "storage class for tier=standard")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := []plugin.Option{
		plugin.WithAddr(*addr),
		plugin.WithHealthAddr(*healthAddr),
		volumes.Plugin(tiers{"fast": *fast, "standard": *standard}),
	}
	if *tlsDir != "" {
		opts = append(opts, plugin.WithMutualTLS(*tlsDir))
	}
	if err := plugin.Serve(ctx, plugin.Info{Name: "tiers", Version: "0.1.0"}, opts...); err != nil {
		slog.Error("plugin stopped", "error", err)
		os.Exit(1)
	}
}
