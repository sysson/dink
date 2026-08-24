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
	"github.com/sysson/dink/pkg/constants"
	"github.com/sysson/dink/pkg/identity"
	"github.com/sysson/dink/pkg/k8s"
	"github.com/sysson/dink/pkg/server"
	"github.com/sysson/dink/pkg/server/middleware"
	"github.com/sysson/dink/pkg/translator"
	"github.com/sysson/dink/pkg/utils/tlsutils"
	"github.com/urfave/cli/v3"
	"google.golang.org/grpc"
)

type Dink struct {
	logger    *slog.Logger
	cfg       *config.Config
	tlsConfig *tls.Config
	handler   *httpHandler
}

func Init(ctx context.Context, cmd *cli.Command) (*Dink, error) {

	cfg := config.NewConfig()
	err := cfg.SetEffectiveConfig(cmd)
	if err != nil {
		return nil, fmt.Errorf("unable to set config :%w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	logFormat := httplog.SchemaOTEL.Concise(version.IsDev())
	logger := slog.New(slog.NewJSONHandler(
		os.Stdout, &slog.HandlerOptions{Level: getLogLevel(cfg.LogLevel), ReplaceAttr: logFormat.ReplaceAttr},
	)).With(
		slog.String("version", version.GetVersion()),
	)

	tlsConfig, err := tlsutils.Config(&tlsutils.TLSConfigOptions{
		DisableTLS:    cfg.DisableTLS,
		TLSCertFile:   cfg.TLSCertFile,
		TLSKeyFile:    cfg.TLSKeyFile,
		MinTLSVersion: cfg.MinTLSVersion,
		ClientCAFile:  cfg.ClientCAFile,
	})
	if err != nil {
		return nil, fmt.Errorf("loading TLS configuration: %w", err)
	}
	if cfg.DisableTLS {
		logger.WarnContext(ctx, "TLS is disabled; serving plaintext only")
	}

	client, err := k8s.New(ctx, &k8s.Options{
		Namespace:      cfg.Namespace,
		KubeConfigPath: cfg.KubeConfigPath,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to create Kubernetes client: %w", err)
	}

	ap := []auth.AuthPlugin{}
	for _, p := range cfg.AuthPlugins {
		ap = append(ap, auth.AuthPlugin{
			Name: p.Name,
			Path: p.Path,
		})
	}

	authChain, err := auth.NewChain(ap)
	if err != nil {
		return nil, fmt.Errorf("configuring auth plugins: %w", err)
	}

	translator := translator.New(client)

	server := server.New(logger)
	server.Use(middleware.RequestID())
	server.Use(middleware.Logging(logger, getLogLevel(cfg.AccessLogLevel)))
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

	return &Dink{
		logger:    logger,
		cfg:       cfg,
		tlsConfig: tlsConfig,
		handler:   handler,
	}, nil
}

func (d *Dink) Run(ctx context.Context) error {
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
			ErrorLog:          slog.NewLogLogger(d.logger.Handler(), getLogLevel(d.cfg.LogLevel)),
			ReadTimeout:       constants.ReadTimeout,
			ReadHeaderTimeout: constants.ReadHeaderTimeout,
			WriteTimeout:      constants.WriteTimeout,
			IdleTimeout:       constants.IdleTimeout,
			Protocols:         proto,
		}
	}

	plainServer := newServer(":"+d.cfg.Port, d.handler, true, true)
	servers := []*http.Server{plainServer}

	serveServer := func(server *http.Server, serve func() error) {
		go func() {
			if err := serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				d.logger.ErrorContext(ctx, "fatal server error", "addr", server.Addr, "error", err)
				execCancel()
			}
		}()
	}

	d.logger.InfoContext(ctx, "Starting plaintext server", "addr", plainServer.Addr)
	serveServer(plainServer, plainServer.ListenAndServe)

	if d.tlsConfig != nil {
		tlsServer := newServer(":"+d.cfg.TLSPort, d.handler, true, false)
		tlsServer.TLSConfig = d.tlsConfig
		servers = append(servers, tlsServer)

		d.logger.InfoContext(ctx, "Starting TLS server", "addr", tlsServer.Addr)
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
			d.logger.ErrorContext(ctx, "error during server shutdown", "addr", server.Addr, "error", err)
			shutdownErr = errors.Join(shutdownErr, err)
		}
	}

	d.logger.InfoContext(ctx, "Server shutdown")
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
