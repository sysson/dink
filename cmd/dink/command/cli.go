package command

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sysson/dink/core/auth"
	"github.com/sysson/dink/core/config"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/registry"
	"github.com/sysson/dink/core/server"
	"github.com/sysson/dink/core/server/middleware"
	"github.com/sysson/dink/core/translator"
	"github.com/sysson/dink/core/version"
	"github.com/sysson/dink/pkg/trap"
	"github.com/sysson/syskit/logx"
	"github.com/sysson/syskit/tlsconfig"
	"github.com/urfave/cli/v3"
	"google.golang.org/grpc"
)

type dinkCLI struct {
	cfg       *config.Config
	tlsConfig *tls.Config
	flags     *[]cli.Flag

	stdOut io.Writer
	stdErr io.Writer

	stopOnce    sync.Once
	apiShutdown chan struct{}
}

func newCLI(opts *options) (*dinkCLI, error) {
	err := loadCLIConfig(opts)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := newTLSConfig(opts.cfg)
	if err != nil {
		return nil, err
	}
	return &dinkCLI{
		cfg:         opts.cfg,
		tlsConfig:   tlsConfig,
		flags:       opts.flags,
		apiShutdown: make(chan struct{}),
	}, nil
}

func loadCLIConfig(opts *options) error {
	fileConfig, err := loadConfigFile(opts.configFile)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		fileConfig = new(config.Config)
	}

	merged := new(config.Config)
	for _, src := range []*config.Config{opts.defaults, fileConfig, opts.cfg} {
		if err := mergeConfig(src, merged); err != nil {
			return err
		}
	}

	if err := merged.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	opts.cfg = merged

	return nil
}

func newTLSConfig(cfg *config.Config) (*tls.Config, error) {
	if cfg.Server.DisableTLS != nil && *cfg.Server.DisableTLS {
		return nil, nil
	}
	tlsVersion, err := config.TLSVersionFromString(cfg.TLS.MinTLSVersion)
	if err != nil {
		return nil, err
	}
	caFiles, err := config.LoadFiles(cfg.TLS.ClientCAFile, cfg.TLS.CertFile, cfg.TLS.KeyFile)
	if err != nil {
		return nil, err
	}
	return tlsconfig.ServerTLSConfig(
		tlsconfig.WithCA(caFiles[0]),
		tlsconfig.WithKeyPair(caFiles[1], caFiles[2]),
		tlsconfig.WithMinVersion(tlsVersion),
	)
}

func newImageService(cfg *config.Config) *registry.ImageService {
	transport, err := newRegistryTransport(cfg)
	if err != nil {
		return registry.Unavailable(fmt.Errorf("registry transport for %q: %w", cfg.Registry.URL, err))
	}
	internal, err := registry.NewClient(cfg.Registry.URL, registry.ClientOptions{Transport: transport})
	if err != nil {
		return registry.Unavailable(fmt.Errorf("registry client for %q: %w", cfg.Registry.URL, err))
	}
	return registry.New(internal)
}

func newRegistryTransport(cfg *config.Config) (http.RoundTripper, error) {
	if strings.HasPrefix(cfg.Registry.URL, "http://") {
		return nil, nil
	}

	caFile := cfg.Registry.CAFile
	if caFile == "" {
		caFile = cfg.TLS.ClientCAFile
	}
	if caFile == "" {
		return nil, nil
	}

	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("no certificates found in %s", caFile)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
	}
	return transport, nil
}

func (c *dinkCLI) start(ctx context.Context) (retErr error) {
	logx.G(ctx).Info("starting up")
	defer func() {
		l := logx.G(ctx)
		if retErr != nil && !errors.Is(retErr, context.Canceled) {
			l = l.WithError(retErr)
		}
		l.Info("shutdown complete")
	}()

	if c.tlsConfig == nil {
		logx.G(ctx).Warn("TLS is disabled; serving plaintext only")
	}

	lss, err := loadListeners(c.cfg, c.tlsConfig)
	if err != nil {
		return fmt.Errorf("unable to load listeners: %w", err)
	}
	translator, err := translator.New(ctx, c.cfg.Kubernetes.SystemNamespace)
	if err != nil {
		return fmt.Errorf("unable to create Kubernetes client: %w", err)
	}

	ap := []auth.AuthPlugin{}
	for _, p := range c.cfg.Auth.Plugins {
		ap = append(ap, auth.AuthPlugin{
			Name: p.Name,
			Path: p.Path,
		})
	}

	authChain, err := auth.NewChain(ap)
	if err != nil {
		return fmt.Errorf("configuring auth plugins: %w", err)
	}

	httpServer := &http.Server{
		ReadHeaderTimeout: 5 * time.Minute,
	}
	healthServer := &http.Server{
		Addr: net.JoinHostPort(c.cfg.Server.Host, c.cfg.Kubernetes.HealthPort),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/livez", "/readyz":
				w.WriteHeader(http.StatusOK)
			default:
				http.NotFound(w, r)
			}
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	apiShutdownCtx, apiShutdownCancel := context.WithCancel(context.WithoutCancel(ctx))
	apiShutdownDone := make(chan struct{})
	trap.T(ctx, 3, c.stop)
	go func() {
		<-c.apiShutdown
		if err := httpServer.Shutdown(apiShutdownCtx); err != nil {
			logx.G(ctx).WithError(err).Error("Error shutting down http server")
		}
		if err := healthServer.Shutdown(apiShutdownCtx); err != nil {
			logx.G(ctx).WithError(err).Error("Error shutting down health server")
		}
		close(apiShutdownDone)
	}()
	defer func() {
		select {
		case <-c.apiShutdown:
			tmr := time.AfterFunc(5*time.Second, apiShutdownCancel)
			defer tmr.Stop()
			<-apiShutdownDone
		default:
			if err := httpServer.Close(); err != nil {
				logx.G(ctx).WithError(err).Error("Error closing http server")
			}
			if err := healthServer.Close(); err != nil {
				logx.G(ctx).WithError(err).Error("Error closing health server")
			}
		}
	}()

	v := version.Get()

	server := server.New()
	server.Use(middleware.RequestID())
	server.Use(middleware.Logging(ctx, c.stdOut, c.cfg.AccessLog.Level))
	server.Use(version.Middleware(v.Version, v.APIVersion, v.MinAPIVersion))
	server.Use(identity.Middleware(identity.MiddlewareConfig{
		DefaultNamespace: c.cfg.Kubernetes.DefaultNamespace,
		SystemNamespace:  c.cfg.Kubernetes.SystemNamespace,
		Ensurer:          translator,
	}))
	server.Use(auth.Middleware(authChain))
	is := newImageService(c.cfg)
	router := buildRouters(translator, is)
	gs := grpc.NewServer()

	proto := new(http.Protocols)
	proto.SetHTTP1(true)
	proto.SetHTTP2(true)
	proto.SetUnencryptedHTTP2(true)
	httpServer.Protocols = proto
	httpServer.Handler = newHTTPHandler(ctx, server.CreateMux(ctx, router...), gs)
	logx.G(ctx).Info("completed initialization;")

	var (
		apiWG      sync.WaitGroup
		errAPI     = make(chan error, 1)
		apiStartWG sync.WaitGroup
	)

	apiStartWG.Add(len(lss) + 1)
	for _, ls := range lss {
		apiWG.Go(func() {
			logx.G(ctx).Info("API listen on", "addr", ls.Addr())
			apiStartWG.Done()
			if err := httpServer.Serve(ls); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logx.G(ctx).With("error", err,
					"listener", ls.Addr(),
				).Error("ServeAPI error")

				select {
				case errAPI <- err:
				default:
				}
			}
		})
	}
	apiWG.Go(func() {
		logx.G(ctx).Info("health listen on", "addr", healthServer.Addr)
		apiStartWG.Done()
		if err := healthServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logx.G(ctx).With("error", err, "listener", healthServer.Addr).Error("ServeHealth error")
			select {
			case errAPI <- err:
			default:
			}
		}
	})
	apiStartWG.Wait()
	apiWG.Wait()
	close(errAPI)

	if err, ok := <-errAPI; ok {
		return fmt.Errorf("shutting down due to ServeAPI error: %w", err)
	}
	return nil
}

func loadListeners(cfg *config.Config, tlsConfig *tls.Config) (listeners []net.Listener, retErr error) {
	defer func() {
		if retErr != nil {
			for _, ls := range listeners {
				_ = ls.Close()
			}
		}
	}()

	if tlsConfig == nil || isBool(cfg.Server.AllowPlaintextWithTLS) {
		ls, err := net.Listen("tcp", net.JoinHostPort(cfg.Server.Host, cfg.Server.Port))
		if err != nil {
			return nil, fmt.Errorf("unable to listen for plaintext server: %w", err)
		}
		listeners = append(listeners, ls)
	}

	if tlsConfig != nil {
		ls, err := net.Listen("tcp", net.JoinHostPort(cfg.Server.Host, cfg.Server.TLSPort))
		if err != nil {
			return nil, fmt.Errorf("unable to listen for TLS server: %w", err)
		}
		listeners = append(listeners, tls.NewListener(ls, tlsConfig))
	}

	if len(listeners) == 0 {
		return nil, errors.New("no listeners configured")
	}

	return listeners, nil
}

func (c *dinkCLI) stop() {
	c.stopOnce.Do(func() {
		close(c.apiShutdown)
	})
}

func isBool(b *bool) bool {
	return b != nil && *b
}
