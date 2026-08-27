package command

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/sysson/dink/dink/pkg/auth"
	"github.com/sysson/dink/dink/pkg/config"
	"github.com/sysson/dink/dink/pkg/identity"
	"github.com/sysson/dink/dink/pkg/k8s"
	"github.com/sysson/dink/dink/pkg/server"
	"github.com/sysson/dink/dink/pkg/server/middleware"
	"github.com/sysson/dink/dink/pkg/translator"
	"github.com/sysson/dink/dink/pkg/utils/log"
	"github.com/sysson/dink/dink/pkg/utils/tlsutils"
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
	if cfg.DisableTLS != nil && *cfg.DisableTLS {
		return nil, nil
	}
	return tlsutils.Config(&tlsutils.TLSConfigOptions{
		DisableTLS:    false,
		TLSCertFile:   cfg.TLSCertFile,
		TLSKeyFile:    cfg.TLSKeyFile,
		MinTLSVersion: cfg.MinTLSVersion,
		ClientCAFile:  cfg.ClientCAFile,
	})
}

func (c *dinkCLI) start(ctx context.Context) (retErr error) {
	log.G(ctx).Info("starting up")
	defer func() {
		l := log.G(ctx)
		if retErr != nil && !errors.Is(retErr, context.Canceled) {
			l = l.WithError(retErr)
		}
		l.Info("shutdown complete")
	}()

	if c.tlsConfig == nil {
		log.G(ctx).Warn("TLS is disabled; serving plaintext only")
	}

	lss, err := loadListeners(c.cfg, c.tlsConfig)
	if err != nil {
		return fmt.Errorf("unable to load listeners: %w", err)
	}

	client, err := k8s.New(ctx, &k8s.Options{
		Namespace:      c.cfg.Namespace,
		KubeConfigPath: c.cfg.KubeConfigPath,
	})
	if err != nil {
		return fmt.Errorf("unable to create Kubernetes client: %w", err)
	}

	ap := []auth.AuthPlugin{}
	for _, p := range c.cfg.AuthPlugins {
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
	apiShutdownCtx, apiShutdownCancel := context.WithCancel(context.WithoutCancel(ctx))
	apiShutdownDone := make(chan struct{})
	trap(ctx, c.stop)
	go func() {
		<-c.apiShutdown
		if err := httpServer.Shutdown(apiShutdownCtx); err != nil {
			log.G(ctx).WithError(err).Error("Error shutting down http server")
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
				log.G(ctx).WithError(err).Error("Error closing http server")
			}
		}
	}()

	server := server.New()
	server.Use(middleware.RequestID())
	server.Use(middleware.Logging(c.stdOut, c.cfg.AccessLogLevel))
	server.Use(middleware.Version(c.cfg.ServerVersion, c.cfg.APIVersion, c.cfg.MinAPIVersion))
	server.Use(identity.Middleware(identity.MiddlewareConfig{
		BaseNamespace:            c.cfg.Namespace,
		DisableNamespaceCreation: isBool(c.cfg.DisableNamespaceCreation),
		Ensurer:                  client,
	}))
	translator := translator.New(client)
	server.Use(auth.Middleware(authChain))
	router := buildRouters(translator)
	gs := grpc.NewServer()

	proto := new(http.Protocols)
	proto.SetHTTP1(true)
	proto.SetHTTP2(true)
	proto.SetUnencryptedHTTP2(true)
	httpServer.Protocols = proto
	httpServer.Handler = newHTTPHandler(ctx, server.CreateMux(ctx, router...), gs)
	log.G(ctx).Info("completed initialization;")

	var (
		apiWG      sync.WaitGroup
		errAPI     = make(chan error, 1)
		apiStartWG sync.WaitGroup
	)

	apiStartWG.Add(len(lss))
	for _, ls := range lss {
		apiWG.Go(func() {
			log.G(ctx).Info("API listen on", "addr", ls.Addr())
			apiStartWG.Done()
			if err := httpServer.Serve(ls); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.G(ctx).With("error", err,
					"listener", ls.Addr(),
				).Error("ServeAPI error")

				select {
				case errAPI <- err:
				default:
				}
			}
		})
	}
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

	if tlsConfig == nil || isBool(cfg.AllowPlaintextWithTLS) {
		ls, err := net.Listen("tcp", net.JoinHostPort(cfg.Host, cfg.Port))
		if err != nil {
			return nil, fmt.Errorf("unable to listen for plaintext server: %w", err)
		}
		listeners = append(listeners, ls)
	}

	if tlsConfig != nil {
		ls, err := net.Listen("tcp", net.JoinHostPort(cfg.Host, cfg.TLSPort))
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
