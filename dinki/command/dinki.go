package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/docker/oci/ocilayout"
	"github.com/docker/oci/ociserver"
	"github.com/go-chi/httplog/v3"
	"github.com/sysson/dink/dinki/registry"
	"github.com/sysson/dink/pkg/trap"
	"github.com/sysson/syskit/logx"
	"github.com/urfave/cli/v3"
)

type Runner interface {
	Run(ctx context.Context, args []string) error
}

type runner struct {
	*cli.Command
}

var (
	stopOnce    = sync.Once{}
	apiShutdown = make(chan struct{})
)

func New(stdout, stderr io.Writer) (Runner, error) {

	logFormat := httplog.SchemaOTEL.Concise(true)
	logLevel := new(slog.LevelVar)
	logger := slog.New(slog.NewJSONHandler(
		stderr, &slog.HandlerOptions{Level: logLevel, ReplaceAttr: logFormat.ReplaceAttr},
	)).With(
		slog.String("version", version),
	)
	slog.SetDefault(logger)
	cmd := &runner{
		Command: &cli.Command{
			Name:      "",
			Usage:     "",
			Writer:    stdout,
			ErrWriter: stderr,
			Action:    run,
		}}
	return cmd, nil
}

func run(ctx context.Context, cmd *cli.Command) (retErr error) {
	logx.G(ctx).Info("starting up")
	defer func() {
		l := logx.G(ctx)
		if retErr != nil && !errors.Is(retErr, context.Canceled) {
			l = l.WithError(retErr)
		}
		l.Info("shutdown complete")
	}()
	backend, err := ocilayout.New(defaultDir, &ocilayout.Options{
		DefaultRepo: defaultRepo,
	})
	if err != nil {
		return err
	}
	mw := []func(http.Handler) http.Handler{
		registry.RequestID(),
		registry.Logging(ctx, cmd.ErrWriter, "info"),
	}
	reg, err := registry.New(backend, &ociserver.ServerConfig{
		Logger:      slog.Default(),
		Middlewares: mw,
	},
	)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		ReadHeaderTimeout: 5 * time.Minute,
	}
	healthServer := &http.Server{
		Addr: net.JoinHostPort(defaultHost, defaultHealthPort),
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
	trap.T(ctx, 3, stop)
	go func() {
		<-apiShutdown
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
		case <-apiShutdown:
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

	proto := new(http.Protocols)
	proto.SetHTTP1(true)
	proto.SetHTTP2(true)
	proto.SetUnencryptedHTTP2(true)
	httpServer.Protocols = proto
	httpServer.Handler = reg
	httpServer.Addr = net.JoinHostPort(defaultHost, defaultPort)
	logx.G(ctx).Info("completed initialization;")

	var (
		apiWG      sync.WaitGroup
		errAPI     = make(chan error, 1)
		apiStartWG sync.WaitGroup
	)

	apiStartWG.Add(2)
	apiWG.Go(func() {
		logx.G(ctx).Info("API listen on", "addr", httpServer.Addr)
		apiStartWG.Done()
		if err := httpServer.ListenAndServeTLS(tlsCert, tlsKey); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logx.G(ctx).With("error", err,
				"listener", httpServer.Addr,
			).Error("ServeAPI error")

			select {
			case errAPI <- err:
			default:
			}
		}
	})
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

func stop() {
	stopOnce.Do(func() {
		close(apiShutdown)
	})
}
