package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/urfave/cli/v3"
)

type nodeOptions struct {
	listen       string
	upstream     string
	registryHost string
	caFile       string
	certsDir     string
	caPoll       time.Duration
}

// newNodeCommand runs on every node, as a hostNetwork DaemonSet. It lets
// containerd pull from dinki as registryHost: it proxies the local endpoint to
// dinki's Service and writes the containerd hosts.toml trusting dinki's CA.
func newNodeCommand() *cli.Command {
	var opts nodeOptions
	return &cli.Command{
		Name:  "serve-node",
		Usage: "Expose dinki to the node's container runtime",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "listen",
				Usage:       "Node-local address containerd pulls from",
				Value:       "127.0.0.1:5000",
				Sources:     cli.EnvVars("DINKI_NODE_LISTEN"),
				Destination: &opts.listen,
			},
			&cli.StringFlag{
				Name:        "upstream",
				Usage:       "dinki Service address to forward connections to",
				Value:       "dinki.dink-system.svc.cluster.local:5000",
				Sources:     cli.EnvVars("DINKI_NODE_UPSTREAM"),
				Destination: &opts.upstream,
			},
			&cli.StringFlag{
				Name:        "registryHost",
				Usage:       "Registry host that image references and pull secrets name",
				Value:       "dinki.io",
				Sources:     cli.EnvVars("DINKI_NODE_REGISTRY_HOST"),
				Destination: &opts.registryHost,
			},
			&cli.StringFlag{
				Name:        "caFile",
				Usage:       "CA bundle that signed dinki's certificate",
				Value:       "/etc/dinki/tls/ca.crt",
				Sources:     cli.EnvVars("DINKI_NODE_CA_FILE"),
				Destination: &opts.caFile,
			},
			&cli.StringFlag{
				Name:        "certsDir",
				Usage:       "containerd registry config_path, mounted at the same path as on the host",
				Value:       "/etc/containerd/certs.d",
				Sources:     cli.EnvVars("DINKI_NODE_CERTS_DIR"),
				Destination: &opts.certsDir,
			},
			&cli.DurationFlag{
				Name:        "caPoll",
				Usage:       "How often to check the CA bundle for rotation",
				Value:       30 * time.Second,
				Sources:     cli.EnvVars("DINKI_NODE_CA_POLL"),
				Destination: &opts.caPoll,
			},
		},
		Action: func(ctx context.Context, _ *cli.Command) error {
			return serveNode(ctx, opts)
		},
	}
}

func serveNode(ctx context.Context, opts nodeOptions) error {
	if opts.caPoll <= 0 {
		return errors.New("caPoll must be positive")
	}
	if err := writeHostsConfig(opts.certsDir, opts.registryHost, opts.listen, opts.caFile); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", opts.listen)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", opts.listen, err)
	}
	slog.InfoContext(ctx, "node proxy listening", "address", listener.Addr(), "upstream", opts.upstream, "registry", opts.registryHost)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		ticker := time.NewTicker(opts.caPoll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := writeHostsConfig(opts.certsDir, opts.registryHost, opts.listen, opts.caFile); err != nil {
					slog.ErrorContext(ctx, "refreshing containerd registry config", "error", err)
				}
			}
		}
	}()
	return proxy(ctx, listener, opts.upstream)
}

// writeHostsConfig writes containerd's hosts.toml and CA for registryHost,
// replacing files only when their content changed.
func writeHostsConfig(certsDir, registryHost, listen, caFile string) error {
	ca, err := os.ReadFile(caFile)
	if err != nil {
		return fmt.Errorf("reading CA bundle: %w", err)
	}
	dir := filepath.Join(certsDir, registryHost)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	caPath := filepath.Join(dir, "ca.crt")
	hosts := fmt.Sprintf(`# Written by dinki serve-node.
server = "https://%[1]s"

[host."https://%[1]s"]
  capabilities = ["pull", "resolve"]
  ca = %[2]q
`, listen, caPath)
	if err := replaceFile(caPath, ca); err != nil {
		return err
	}
	return replaceFile(filepath.Join(dir, "hosts.toml"), []byte(hosts))
}

// replaceFile atomically replaces path with data unless it already holds it.
func replaceFile(path string, data []byte) error {
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, data) {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// proxy forwards each connection accepted on listener to upstream, byte for
// byte, so TLS stays end to end with dinki. It returns when ctx ends.
func proxy(ctx context.Context, listener net.Listener, upstream string) error {
	var connections sync.WaitGroup
	defer connections.Wait()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	for {
		client, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accepting node connection: %w", err)
		}
		connections.Go(func() {
			defer func() { _ = client.Close() }()
			server, err := dialer.DialContext(ctx, "tcp", upstream)
			if err != nil {
				slog.ErrorContext(ctx, "connecting to dinki", "upstream", upstream, "error", err)
				return
			}
			defer func() { _ = server.Close() }()
			stop := context.AfterFunc(ctx, func() {
				_ = client.Close()
				_ = server.Close()
			})
			defer stop()
			var copies sync.WaitGroup
			copies.Go(func() { pipe(server, client) })
			pipe(client, server)
			copies.Wait()
		})
	}
}

// pipe copies src to dst, then half-closes dst so the peer sees EOF.
func pipe(dst, src net.Conn) {
	_, _ = io.Copy(dst, src)
	if conn, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = conn.CloseWrite()
		return
	}
	_ = dst.Close()
}
