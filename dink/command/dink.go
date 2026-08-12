package command

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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
	"github.com/sysson/dink/internal/config"
	"github.com/sysson/dink/internal/k8s"
	"github.com/sysson/dink/internal/server"
	"github.com/sysson/dink/internal/server/middleware"
	"github.com/sysson/dink/internal/translator"
	"google.golang.org/grpc"
)

const (
	defaultReadTimeout       = 60 * time.Second
	defaultReadHeaderTimeout = 60 * time.Second
	defaultWriteTimeout      = 0
	defaultIdleTimeout       = 120 * time.Second
)

type tlsConfig struct {
	RootCA       *x509.CertPool
	Certificates []tls.Certificate
}

func Dink(ctx context.Context, config *config.Config) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	logFormat := httplog.SchemaOTEL.Concise(version.IsDev())
	logger := slog.New(slog.NewJSONHandler(
		os.Stdout, &slog.HandlerOptions{Level: getLogLevel(config.LogLevel), ReplaceAttr: logFormat.ReplaceAttr},
	)).With(
		slog.String("version", version.Version),
	)

	client, err := k8s.New(ctx, config)
	if err != nil {
		return fmt.Errorf("unable to create Kubernetes client: %w", err)
	}

	translator := translator.New(client)

	server := server.New(logger)
	server.Use(middleware.Logging(logger))
	server.Use(middleware.RequestID())
	server.Use(middleware.Version(config.ServerVersion, config.APIVersion, config.MinAPIVersion))

	router := buildRouters(translator)
	api := server.CreateMux(ctx, router...)
	gs := grpc.NewServer()
	handler := newHTTPHandler(ctx, api, gs)

	tls := new(tlsConfig)
	if !config.DisableTLS {
		tlsCert, err := client.LoadOrCreateServerTLS(ctx)
		if err != nil {
			return fmt.Errorf("loading or creating server TLS: %w", err)
		}
		tls.Certificates = append(tls.Certificates, *tlsCert)
		tls.RootCA, err = client.GetRootCA(ctx)
		if err != nil {
			return fmt.Errorf("getting root CA: %w", err)
		}
	}

	return serve(ctx, logger, config, tls, handler)
}

func serve(
	ctx context.Context,
	logger *slog.Logger,
	config *config.Config,
	tlsConfig *tlsConfig,
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
			ErrorLog:          slog.NewLogLogger(logger.Handler(), getLogLevel(config.LogLevel)),
			ReadTimeout:       defaultReadTimeout,
			ReadHeaderTimeout: defaultReadHeaderTimeout,
			WriteTimeout:      defaultWriteTimeout,
			IdleTimeout:       defaultIdleTimeout,
			Protocols:         proto,
		}
	}

	plainServer := newServer(":"+config.Port, handler, true, true)
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

	if !config.DisableTLS {
		tlsServer := newServer(":"+config.TLSPort, handler, true, false)
		tlsServer.TLSConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			Certificates:       tlsConfig.Certificates,
			RootCAs:            tlsConfig.RootCA,
			ClientAuth:         tls.RequireAndVerifyClientCert,
			InsecureSkipVerify: false,
			NextProtos:         []string{"h2", "http/1.1"},
		}
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
