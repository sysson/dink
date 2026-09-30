// Command secrets-plugin is an example dink secrets plugin that reads values
// from files laid out as <root>/<namespace>/<key>, e.g. a mounted Secret per tenant.
//
// A container started with FOO=se://files/db/password receives the contents
// of <root>/<caller's namespace>/db/password as FOO.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sysson/dink/sdk/plugin"
	"github.com/sysson/dink/sdk/secrets"
)

type files struct {
	root string
}

func (f files) Resolve(_ context.Context, req *secrets.Request) ([]secrets.Secret, error) {
	out := make([]secrets.Secret, 0, len(req.Refs))
	for _, ref := range req.Refs {
		value, err := f.read(req.Namespace, ref.Ref)
		out = append(out, secrets.Secret{Name: ref.Name, Value: value, Err: err})
	}
	return out, nil
}

func (f files) read(namespace, key string) (string, error) {
	// Keep every lookup inside the caller's own namespace directory.
	if namespace == "" || !filepath.IsLocal(namespace) || !filepath.IsLocal(key) {
		return "", errors.New("invalid key")
	}
	b, err := os.ReadFile(filepath.Join(f.root, namespace, key))
	if errors.Is(err, os.ErrNotExist) {
		return "", errors.New("not found")
	}
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}

func main() {
	addr := flag.String("addr", plugin.DefaultAddr, "listen address, host:port or unix:///path")
	root := flag.String("root", "/var/run/secrets/files", "directory holding one subdirectory per namespace")
	tlsDir := flag.String("tls-dir", "", "directory holding tls.crt, tls.key and ca.crt; empty serves plaintext")
	healthAddr := flag.String("health-addr", ":8081", "plaintext address for /livez and /readyz")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := []plugin.Option{
		plugin.WithAddr(*addr),
		plugin.WithHealthAddr(*healthAddr),
		secrets.Plugin(files{root: *root}),
	}
	if *tlsDir != "" {
		opts = append(opts, plugin.WithMutualTLS(*tlsDir))
	}
	if err := plugin.Serve(ctx, plugin.Info{Name: "files", Version: "0.1.0"}, opts...); err != nil {
		slog.Error("plugin stopped", "error", err)
		os.Exit(1)
	}
}
