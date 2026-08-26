package command

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/sysson/dink/dink/pkg/auth"
	"github.com/sysson/dink/dink/pkg/config"
	"github.com/sysson/dink/dink/pkg/constants"
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
	cfg, err := config.Load(opts.configFile)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		cfg = config.New()
	}

	if err := mergeConfig(cfg, opts); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	opts.cfg = cfg

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

	translator := translator.New(client)

	server := server.New()
	server.Use(middleware.RequestID())
	server.Use(middleware.Logging(c.stdOut, getLogLevel(c.cfg.AccessLogLevel)))
	server.Use(middleware.Version(c.cfg.ServerVersion, c.cfg.APIVersion, c.cfg.MinAPIVersion))
	server.Use(identity.Middleware(identity.MiddlewareConfig{
		BaseNamespace:            c.cfg.Namespace,
		DisableNamespaceCreation: isBool(c.cfg.DisableNamespaceCreation),
		Ensurer:                  client,
	}))
	server.Use(auth.Middleware(authChain))

	trap(ctx, c.stop)

	router := buildRouters(translator)
	api := server.CreateMux(ctx, router...)
	gs := grpc.NewServer()
	handler := newHTTPHandler(ctx, api, gs)

	proto := new(http.Protocols)
	proto.SetHTTP1(true)
	proto.SetHTTP2(true)
	proto.SetUnencryptedHTTP2(true)
	httpServer := &http.Server{
		Handler:           handler,
		ReadTimeout:       constants.ReadTimeout,
		ReadHeaderTimeout: constants.ReadHeaderTimeout,
		WriteTimeout:      constants.WriteTimeout,
		IdleTimeout:       constants.IdleTimeout,
		Protocols:         proto,
	}

	listeners := []net.Listener{}
	closeListeners := func() {
		for _, listener := range listeners {
			if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				log.G(ctx).WithError(err).Error("error closing listener", "addr", listener.Addr())
			}
		}
	}
	defer closeListeners()

	listen := func(addr string) (net.Listener, error) {
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, err
		}
		return listener, nil
	}

	serveListener := func(listener net.Listener) {
		go func() {
			if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.G(ctx).WithError(err).Error("fatal server error", "addr", listener.Addr())
				c.stop()
			}
		}()
	}

	startPlaintext := c.tlsConfig == nil || isBool(c.cfg.AllowPlaintextWithTLS)
	if startPlaintext {
		listener, err := listen(":" + c.cfg.Port)
		if err != nil {
			return fmt.Errorf("unable to listen for plaintext server: %w", err)
		}
		listeners = append(listeners, listener)

		log.G(ctx).Info("starting plaintext server", "addr", listener.Addr())
		serveListener(listener)
	} else {
		log.G(ctx).Info("plaintext listener disabled while TLS is enabled")
	}

	if c.tlsConfig != nil {
		listener, err := listen(":" + c.cfg.TLSPort)
		if err != nil {
			return fmt.Errorf("unable to listen for TLS server: %w", err)
		}
		listener = tls.NewListener(listener, c.tlsConfig)
		listeners = append(listeners, listener)

		log.G(ctx).Info("starting TLS server", "addr", listener.Addr())
		serveListener(listener)
	}

	<-c.apiShutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Minute)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.G(ctx).WithError(err).Error("error during server shutdown")
	}

	log.G(ctx).Info("server shutdown")

	return nil
}

func (c *dinkCLI) stop() {
	c.stopOnce.Do(func() {
		close(c.apiShutdown)
	})
}

func getLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func isBool(b *bool) bool {
	return b != nil && *b
}
