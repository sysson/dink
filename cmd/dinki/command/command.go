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
	"strings"
	"time"

	"github.com/sysson/dink/cmd/dinki/config"
	"github.com/sysson/dink/core/registry"
	api "github.com/sysson/dink/core/registry/api/server"
	"github.com/sysson/dink/core/registry/pullauth"
	"github.com/sysson/dink/core/registry/server"
	"github.com/sysson/dink/core/server/middleware"
	"github.com/sysson/dink/core/version"
	"github.com/sysson/ocistore"
	"github.com/sysson/ocistore/blobstore"
	"github.com/sysson/ocistore/blobstore/fileblob"
	"github.com/sysson/ocistore/kv/boltkv"
	"github.com/sysson/ocistore/kvmeta"
	"github.com/sysson/ocistore/query"
	"github.com/sysson/syskit/logx"
	"github.com/urfave/cli/v3"
)

type Runner interface {
	Run(ctx context.Context, args []string) error
}

func New(stdout, stderr io.Writer) Runner {
	var configFile string
	build := version.Get()
	command := &cli.Command{
		Name:      "dinki",
		Usage:     "Run dink's OCI registry",
		Version:   fmt.Sprintf("%s (commit %s, built %s, dirty %t)", build.Version, build.Commit, build.Date, build.Dirty),
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
		Commands: []*cli.Command{newNodeCommand()},
	}
	return command
}

func serve(ctx context.Context, cfg config.Config, stderr io.Writer) error {
	level := new(slog.LevelVar)
	if err := level.UnmarshalText([]byte(cfg.Log.Level)); err != nil {
		return fmt.Errorf("log.level: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: level}))
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
			collectGarbage(collectorCtx, backend.registry, cfg.GC)
		}()
		defer func() {
			cancelCollector()
			<-collectorDone
		}()
	}
	handler, err := newHandler(cfg, backend)
	if err != nil {
		return err
	}
	tlsConfig, err := serverTLSConfig(cfg)
	if err != nil {
		return err
	}
	if tlsConfig == nil {
		slog.WarnContext(ctx, "TLS is disabled; serving the registry in plaintext")
	}
	return serveAll(ctx, []*listenerServer{
		{
			name:    "registry",
			address: net.JoinHostPort(cfg.Server.Host, cfg.Server.Port),
			tls:     tlsConfig,
			handler: requestMiddleware(ctx, cfg, stderr, handler),
		},
		{
			name:    "health",
			address: net.JoinHostPort(cfg.Server.Host, cfg.Server.HealthPort),
			handler: healthHandler(),
		},
	})
}

func healthHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// newHandler serves read-only node access and repository-scoped build uploads.
// The internal API and GraphQL require dink's client certificate.
func newHandler(cfg config.Config, backend *registryBackend) (http.Handler, error) {
	store := backend.registry
	ociHandler, err := server.New(pullauth.Scope(store))
	if err != nil {
		return nil, err
	}
	registryHandler := pullauth.Middleware(backend.credentials)(ociHandler)
	if cfg.API.Disabled {
		return registryHandler, nil
	}
	index := store.Index()
	queries, err := query.New(index)
	if err != nil {
		return nil, err
	}
	apiServer, err := api.New(registry.New(store, index), queries, backend.credentials)
	if err != nil {
		return nil, err
	}
	path, apiHandler := apiServer.Handler()
	mux := http.NewServeMux()
	mux.Handle(path, apiHandler)
	prefixes := []string{path}
	if cfg.GraphQL.Enabled {
		mux.Handle(server.GraphQLPath, queries)
		prefixes = append(prefixes, server.GraphQLPath)
	}
	return &router{
		registry:    registryHandler,
		api:         requireAPIClient(cfg.API.ClientOrganization, cfg.API.ClientCommonName, mux),
		apiPrefixes: prefixes,
	}, nil
}

// router sends requests under apiPrefixes to api and the rest to registry.
type router struct {
	registry    http.Handler
	api         http.Handler
	apiPrefixes []string
}

func (h *router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for _, prefix := range h.apiPrefixes {
		if strings.HasPrefix(r.URL.Path, prefix) {
			h.api.ServeHTTP(w, r)
			return
		}
	}
	h.registry.ServeHTTP(w, r)
}

// requireAPIClient admits only requests authenticated by a verified client
// certificate whose subject names dink.
func requireAPIClient(organization, commonName string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := verifyAPIClient(r.TLS, organization, commonName); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
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
// disabled. With the API enabled it verifies client certificates that are
// offered; requireAPIClient decides per request which ones reach the API.
func serverTLSConfig(cfg config.Config) (*tls.Config, error) {
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
	if !cfg.API.Disabled {
		clientCAFile := cfg.API.ClientCAFile
		pem, err := os.ReadFile(clientCAFile)
		if err != nil {
			return nil, fmt.Errorf("reading API client CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in %s", clientCAFile)
		}
		config.ClientCAs = pool
		config.ClientAuth = tls.VerifyClientCertIfGiven
	}
	return config, nil
}

func serveAll(ctx context.Context, servers []*listenerServer) error {
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
		slog.InfoContext(ctx, s.name+" listening", "address", listener.Addr(), "storage", "file", "metadata", "bbolt")
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

var _ api.Querier = (*query.Service)(nil)

// collectGarbage runs a collection pass every cfg.Interval until ctx ends.
func collectGarbage(ctx context.Context, store *ocistore.Store, cfg config.GC) {
	ticker := time.NewTicker(time.Duration(cfg.Interval))
	defer ticker.Stop()
	for {
		cutoff := time.Now().Add(-time.Duration(cfg.UploadExpiry))
		if err := store.CollectGarbage(ctx); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "collecting unreferenced registry content", "error", err)
		}
		if err := store.CleanupExpiredUploads(ctx, cutoff); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "cleaning expired registry uploads", "error", err)
		}
		if err := store.CleanupExpiredReservations(ctx, cutoff); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "cleaning expired registry content reservations", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type registryBackend struct {
	registry    *ocistore.Store
	content     *blobstore.Store
	metadata    *kvmeta.Store
	credentials *pullauth.Store
}

func (b *registryBackend) Close() error {
	return errors.Join(b.metadata.Close(), b.content.Close())
}

// newBackend opens local blob and metadata stores, including pull credentials.
func newBackend(ctx context.Context, cfg config.Config) (*registryBackend, error) {
	content, err := fileblob.Open(ctx, cfg.Storage.Path)
	if err != nil {
		return nil, fmt.Errorf("opening blob storage: %w", err)
	}
	store, err := (boltkv.Config{Path: cfg.Metadata.Path}).Open(ctx)
	if err != nil {
		_ = content.Close()
		return nil, fmt.Errorf("opening metadata store: %w", err)
	}
	metadata, err := kvmeta.New(ctx, store)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("opening metadata store: %w", err), store.Close(), content.Close())
	}
	registry, err := ocistore.New(content, metadata, ocistore.WithAllowMissingManifestChildren())
	if err != nil {
		return nil, errors.Join(err, content.Close(), metadata.Close())
	}
	return &registryBackend{registry: registry, content: content, metadata: metadata, credentials: pullauth.New(store)}, nil
}

// verifyAPIClient accepts only a verified client certificate whose subject
// matches organization and commonName (an empty value is not checked).
func verifyAPIClient(state *tls.ConnectionState, organization, commonName string) error {
	if state == nil || len(state.VerifiedChains) == 0 || len(state.VerifiedChains[0]) == 0 {
		return errors.New("internal registry API requires a client certificate")
	}
	subject := state.VerifiedChains[0][0].Subject
	if commonName != "" && subject.CommonName != commonName {
		return fmt.Errorf("client certificate %q is not authorized for the internal registry API", subject.CommonName)
	}
	if organization != "" && !slices.Contains(subject.Organization, organization) {
		return fmt.Errorf("client certificate %q is not authorized for the internal registry API", subject.CommonName)
	}
	return nil
}
