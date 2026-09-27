package command

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/sysson/dink/cmd/dinki/config"
	"github.com/sysson/dink/core/registry"
	api "github.com/sysson/dink/core/registry/api/server"
	"github.com/sysson/dink/core/registry/backend/blobstore"
	"github.com/sysson/dink/core/registry/backend/kvmeta"
	"github.com/sysson/dink/core/registry/ocibackend"
	"github.com/sysson/dink/core/registry/query"
	"github.com/sysson/dink/core/registry/server"
	"github.com/sysson/dink/core/server/middleware"
	"github.com/sysson/syskit/logx"
	"github.com/urfave/cli/v3"
)

type Runner interface {
	Run(ctx context.Context, args []string) error
}

func New(stdout, stderr io.Writer) Runner {
	var configFile string
	command := &cli.Command{
		Name:      "dinki",
		Usage:     "Run dink's OCI registry",
		Writer:    stdout,
		ErrWriter: stderr,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "config",
				Usage:       "Path to the dinki configuration file",
				Value:       config.DefaultFile,
				Sources:     cli.EnvVars("DINKI_CONFIG"),
				Destination: &configFile,
			},
		},
		Action: func(ctx context.Context, _ *cli.Command) error {
			cfg, err := config.Load(configFile)
			if err != nil {
				return err
			}
			return serve(ctx, cfg, stderr)
		},
	}
	return command
}

func serve(ctx context.Context, cfg config.Config, stderr io.Writer) error {
	level := new(slog.LevelVar)
	if err := level.UnmarshalText([]byte(cfg.Log.Level)); err != nil {
		return fmt.Errorf("log.level: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)
	logx.SetDefault(logger)

	backend, err := newBackend(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() {
		if err := backend.Close(); err != nil {
			slog.ErrorContext(ctx, "closing registry backend", "error", err)
		}
	}()
	{
		collectorCtx, cancelCollector := context.WithCancel(ctx)
		collectorDone := make(chan struct{})
		go func() {
			defer close(collectorDone)
			backend.registry.RunGarbageCollector(collectorCtx, time.Minute)
		}()
		defer func() {
			cancelCollector()
			<-collectorDone
		}()
	}
	queries, err := query.New(backend.metadata, backend.content)
	if err != nil {
		return err
	}
	handler, err := server.New(backend.registry)
	if err != nil {
		return err
	}
	handler = requestMiddleware(ctx, cfg, stderr, handler)

	servers := []*listenerServer{}
	registryTLS, err := serverTLSConfig(cfg, nil)
	if err != nil {
		return err
	}
	servers = append(servers, &listenerServer{
		name:    "registry",
		address: net.JoinHostPort(cfg.Server.Host, cfg.Server.Port),
		tls:     registryTLS,
		handler: handler,
	})
	if !cfg.API.Disabled {
		apiServer, err := api.New(registry.New(backend.registry, queries), queries)
		if err != nil {
			return err
		}
		path, apiHandler := apiServer.Handler()
		mux := http.NewServeMux()
		mux.Handle(path, apiHandler)
		if cfg.GraphQL.Enabled {
			mux.Handle(server.GraphQLPath, queries)
		}
		apiTLS, err := serverTLSConfig(cfg, &cfg.API)
		if err != nil {
			return err
		}
		if apiTLS == nil {
			slog.WarnContext(ctx, "internal registry API is served without TLS or client authentication")
		}
		internalHandler := requestMiddleware(ctx, cfg, stderr, mux)
		servers = append(servers, &listenerServer{
			name:    "internal registry API",
			address: net.JoinHostPort(cfg.API.Host, cfg.API.Port),
			tls:     apiTLS,
			handler: internalHandler,
		})
	}
	return serveAll(ctx, cfg, servers)
}

func requestMiddleware(ctx context.Context, cfg config.Config, stderr io.Writer, handler http.Handler) http.Handler {
	if cfg.AccessLog.Enabled {
		handler = middleware.Logging(ctx, stderr, cfg.AccessLog.Level)(handler)
	} else {
		handler = middleware.ContextLogger()(handler)
	}
	return middleware.RequestID()(handler)
}

type listenerServer struct {
	name    string
	address string
	tls     *tls.Config
	handler http.Handler
}

// serverTLSConfig returns the listener TLS configuration, or nil when TLS is
// disabled. A clientCAFile makes the listener require and verify client
// certificates.
// serverTLSConfig builds a listener TLS config. With api set it also requires a
// client certificate from api.ClientCAFile whose subject names dink.
func serverTLSConfig(cfg config.Config, api *config.API) (*tls.Config, error) {
	if cfg.TLS.Disabled {
		return nil, nil
	}
	certificate, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("loading registry TLS key pair: %w", err)
	}
	minVersion, err := config.TLSVersion(cfg.TLS.MinTLSVersion)
	if err != nil {
		return nil, err
	}
	config := &tls.Config{
		MinVersion:   minVersion,
		Certificates: []tls.Certificate{certificate},
	}
	if api != nil {
		clientCAFile := api.ClientCAFile
		pem, err := os.ReadFile(clientCAFile)
		if err != nil {
			return nil, fmt.Errorf("reading API client CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in %s", clientCAFile)
		}
		config.ClientCAs = pool
		config.ClientAuth = tls.RequireAndVerifyClientCert
		config.VerifyConnection = verifyAPIClient(api.ClientOrganization, api.ClientCommonName)
	}
	return config, nil
}

func serveAll(ctx context.Context, cfg config.Config, servers []*listenerServer) error {
	type served struct {
		name string
		err  error
	}
	results := make(chan served, len(servers))
	httpServers := make([]*http.Server, 0, len(servers))
	defer func() {
		for _, server := range httpServers {
			_ = server.Close()
		}
	}()
	for _, s := range servers {
		listener, err := net.Listen("tcp", s.address)
		if err != nil {
			return fmt.Errorf("listening for %s requests: %w", s.name, err)
		}
		protocols := new(http.Protocols)
		protocols.SetHTTP1(true)
		if s.tls != nil {
			protocols.SetHTTP2(true)
			s.tls.NextProtos = []string{"h2", "http/1.1"}
			listener = tls.NewListener(listener, s.tls)
		} else {
			protocols.SetUnencryptedHTTP2(true)
		}
		server := &http.Server{
			Handler:           s.handler,
			ReadHeaderTimeout: 10 * time.Second,
			Protocols:         protocols,
		}
		httpServers = append(httpServers, server)
		go func(name string) {
			results <- served{name: name, err: server.Serve(listener)}
		}(s.name)
		slog.InfoContext(ctx, s.name+" listening", "address", listener.Addr(), "storage", driverName(cfg.Storage.Driver()), "metadata", driverName(cfg.Metadata.Driver()))
	}

	var failure error
	select {
	case result := <-results:
		if result.err != nil && !errors.Is(result.err, http.ErrServerClosed) {
			failure = fmt.Errorf("serving %s requests: %w", result.name, result.err)
		}
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	for _, server := range httpServers {
		if err := server.Shutdown(shutdownCtx); err != nil && failure == nil {
			failure = fmt.Errorf("shutting down: %w", err)
		}
	}
	return failure
}

var _ registry.Querier = (*query.Service)(nil)

type registryBackend struct {
	registry *ocibackend.Registry
	content  *blobstore.Store
	metadata *kvmeta.Store
}

func (b *registryBackend) Close() error {
	return errors.Join(b.metadata.Close(), b.content.Close())
}

// newBackend opens the blob driver selected by storage and the kv metadata
// driver selected by metadata.
func newBackend(ctx context.Context, cfg config.Config) (*registryBackend, error) {
	content, err := blobstore.OpenConfig(ctx, cfg.Storage)
	if err != nil {
		return nil, fmt.Errorf("opening blob storage: %w", err)
	}
	metadata, err := kvmeta.Open(ctx, cfg.Metadata)
	if err != nil {
		_ = content.Close()
		return nil, fmt.Errorf("opening metadata store: %w", err)
	}
	registry, err := ocibackend.New(content, metadata)
	if err != nil {
		return nil, errors.Join(err, content.Close(), metadata.Close())
	}
	return &registryBackend{registry: registry, content: content, metadata: metadata}, nil
}

// verifyAPIClient accepts only the verified client certificate whose subject
// matches organization and commonName (an empty value is not checked).
func verifyAPIClient(organization, commonName string) func(tls.ConnectionState) error {
	return func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return errors.New("internal registry API requires a client certificate")
		}
		subject := state.PeerCertificates[0].Subject
		if commonName != "" && subject.CommonName != commonName {
			return fmt.Errorf("client certificate %q is not authorized for the internal registry API", subject.CommonName)
		}
		if organization != "" && !slices.Contains(subject.Organization, organization) {
			return fmt.Errorf("client certificate %q is not authorized for the internal registry API", subject.CommonName)
		}
		return nil
	}
}

func driverName[T any](name string, _ T, _ error) string { return name }
