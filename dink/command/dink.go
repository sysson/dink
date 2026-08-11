package command

import (
	"context"
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
	"github.com/sysson/dink/internal/server/router"
	"github.com/sysson/dink/internal/server/router/container"
	"github.com/sysson/dink/internal/translator"
	"google.golang.org/grpc"
)

const (
	defaultReadTimeout       = 5 * time.Second
	defaultReadHeaderTimeout = 5 * time.Second
	defaultWriteTimeout      = 10 * time.Second
	defaultIdleTimeout       = 120 * time.Second
)

type Dink struct {
	config *config.Config
}

func New(config *config.Config) *Dink {
	return &Dink{
		config: config,
	}
}

func (d *Dink) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	logFormat := httplog.SchemaOTEL.Concise(version.IsDev())
	logger := slog.New(slog.NewJSONHandler(
		os.Stdout, &slog.HandlerOptions{Level: getLogLevel(d.config.LogLevel), ReplaceAttr: logFormat.ReplaceAttr},
	)).With(
		slog.String("version", version.Version),
	)

	client, err := k8s.New(ctx, d.config)
	if err != nil {
		return fmt.Errorf("unable to create Kubernetes client: %w", err)
	}

	translator := translator.New(client)

	server := server.New(logger)
	server.Use(middleware.Logging(logger))

	router := buildRouters(translator)
	api := server.CreateMux(ctx, router...)
	gs := grpc.NewServer()
	handler := newHTTPHandler(ctx, api, gs)

	return serve(ctx, logger, d.config, handler)
}

func buildRouters(t *translator.Translator) []router.Router {
	return []router.Router{
		container.New(t),
	}
}

func serve(
	ctx context.Context,
	logger *slog.Logger,
	config *config.Config,
	r http.Handler,
) error {
	execCtx, execCancel := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer execCancel()
	proto := new(http.Protocols)
	proto.SetHTTP1(true)
	proto.SetHTTP2(true)
	proto.SetUnencryptedHTTP2(true)
	server := http.Server{
		Addr:              ":" + config.Port,
		Handler:           r,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), getLogLevel(config.LogLevel)),
		ReadTimeout:       defaultReadTimeout,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		WriteTimeout:      defaultWriteTimeout,
		IdleTimeout:       defaultIdleTimeout,
		Protocols:         proto,
	}
	logger.InfoContext(ctx, "Starting server", "address", config.Port, "tlsOnly", config.TLSOnly, "tlsPort", config.TLSPort)

	go func() {
		if err := server.ListenAndServe(); err != nil {
			if !errors.Is(err, http.ErrServerClosed) {
				logger.ErrorContext(ctx, "fatal server error", "error", err)
				execCancel()
			} else {
				logger.InfoContext(ctx, "Recieved signal, shutting down...")
			}
			return
		}
	}()

	<-execCtx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Minute)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.ErrorContext(ctx, "error during server shutdown", "error", err)
		return err
	}

	logger.InfoContext(ctx, "Server shutdown")
	return nil
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
