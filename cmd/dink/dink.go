package dink

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/httplog/v3"
	"github.com/sysson/dink/cmd/version"
	"github.com/sysson/dink/pkg/auth"
	"github.com/sysson/dink/pkg/config"
	"github.com/sysson/dink/pkg/identity"
	"github.com/sysson/dink/pkg/k8s"
	"github.com/sysson/dink/pkg/server"
	"github.com/sysson/dink/pkg/server/middleware"
	"github.com/sysson/dink/pkg/translator"
	"github.com/sysson/dink/pkg/utils/tlsconfig"
	"github.com/urfave/cli/v3"
	"google.golang.org/grpc"
)

const (
	defaultReadTimeout       = 60 * time.Second
	defaultReadHeaderTimeout = 60 * time.Second
	defaultWriteTimeout      = 0
	defaultIdleTimeout       = 120 * time.Second
)

func dink(ctx context.Context, cmd *cli.Command) error {

	cfg := config.NewConfig()
	err := cfg.SetEffectiveConfig(cmd)
	if err != nil {
		return fmt.Errorf("unable to set config :%w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	logFormat := httplog.SchemaOTEL.Concise(version.IsDev())
	logger := slog.New(slog.NewJSONHandler(
		os.Stdout, &slog.HandlerOptions{Level: getLogLevel(cfg.LogLevel), ReplaceAttr: logFormat.ReplaceAttr},
	)).With(
		slog.String("version", version.Version),
	)

	tlsConfig, err := tlsconfig.Load(&tlsconfig.TLSConfigOptions{
		DisableTLS:    cfg.DisableTLS,
		TLSCertFile:   cfg.TLSCertFile,
		TLSKeyFile:    cfg.TLSKeyFile,
		MinTLSVersion: cfg.MinTLSVersion,
		ClientCAFile:  cfg.ClientCAFile,
	})
	if err != nil {
		return fmt.Errorf("loading TLS configuration: %w", err)
	}
	if cfg.DisableTLS {
		logger.WarnContext(ctx, "TLS is disabled; serving plaintext only")
	}

	client, err := k8s.New(ctx, cfg)
	if err != nil {
		return fmt.Errorf("unable to create Kubernetes client: %w", err)
	}

	authChain, err := auth.NewChain(cfg.AuthPlugins)
	if err != nil {
		return fmt.Errorf("configuring auth plugins: %w", err)
	}

	translator := translator.New(client)

	server := server.New(logger)
	server.Use(middleware.Logging(logger))
	server.Use(middleware.RequestID())
	server.Use(middleware.Version(cfg.ServerVersion, cfg.APIVersion, cfg.MinAPIVersion))
	server.Use(identity.Middleware(identity.MiddlewareConfig{
		BaseNamespace:            cfg.Namespace,
		DisableNamespaceCreation: cfg.DisableNamespaceCreation,
		Ensurer:                  client,
		Logger:                   logger,
	}))
	server.Use(auth.Middleware(authChain))

	router := buildRouters(translator)
	api := server.CreateMux(ctx, router...)
	gs := grpc.NewServer()
	handler := newHTTPHandler(ctx, api, gs)

	return serve(ctx, logger, cfg, tlsConfig, handler)
}

func serve(
	ctx context.Context,
	logger *slog.Logger,
	cfg *config.Config,
	tlsConfig *tls.Config,
	handler http.Handler,
) error {
	execCtx, execCancel := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer execCancel()
	newServer := func(addr string, serverHandler http.Handler, enableHTTP2, enableUnencryptedHTTP2 bool) *http.Server {
		proto := new(http.Protocols)
		proto.SetHTTP1(true)
		proto.SetHTTP2(enableHTTP2)
		proto.SetUnencryptedHTTP2(enableUnencryptedHTTP2)
		return &http.Server{
			Addr:              addr,
			Handler:           serverHandler,
			ErrorLog:          slog.NewLogLogger(logger.Handler(), getLogLevel(cfg.LogLevel)),
			ReadTimeout:       defaultReadTimeout,
			ReadHeaderTimeout: defaultReadHeaderTimeout,
			WriteTimeout:      defaultWriteTimeout,
			IdleTimeout:       defaultIdleTimeout,
			Protocols:         proto,
		}
	}

	plainServer := newServer(":"+cfg.Port, handler, true, true)
	servers := []*http.Server{plainServer}

	serveServer := func(server *http.Server, serve func() error) {
		go func() {
			if err := serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.ErrorContext(ctx, "fatal server error", "addr", server.Addr, "error", err)
				execCancel()
			}
		}()
	}

	logger.InfoContext(ctx, "Starting plaintext server", "addr", plainServer.Addr)
	serveServer(plainServer, plainServer.ListenAndServe)

	if tlsConfig != nil {
		tlsServer := newServer(":"+cfg.TLSPort, handler, true, false)
		tlsServer.TLSConfig = tlsConfig
		servers = append(servers, tlsServer)

		logger.InfoContext(ctx, "Starting TLS server", "addr", tlsServer.Addr)
		serveServer(tlsServer, func() error {
			return tlsServer.ListenAndServeTLS("", "")
		})
	}

	<-execCtx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Minute)
	defer shutdownCancel()

	var shutdownErr error
	for _, server := range servers {
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.ErrorContext(ctx, "error during server shutdown", "addr", server.Addr, "error", err)
			shutdownErr = errors.Join(shutdownErr, err)
		}
	}

	logger.InfoContext(ctx, "Server shutdown")
	return shutdownErr
}

func getLogLevel(loglevel string) slog.Level {
	switch loglevel {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
