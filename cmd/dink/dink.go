package dink

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

	"github.com/sysson/dink/internal/config"
	"github.com/sysson/dink/internal/k8s"
	"github.com/sysson/dink/internal/server"
	"github.com/urfave/cli/v3"
)

var version = "dev"

func RootCmd() *cli.Command {
	return &cli.Command{
		Name:    "dink",
		Usage:   "A Kubernetes API compatibility proxy",
		Version: version,
		Action:  dink,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "config",
				Value: "",
				Usage: "Path to the configuration file",
			},
		},
	}
}

func dink(ctx context.Context, cmd *cli.Command) error {
	cfg := config.NewConfig()
	err := cfg.SetEffectiveConfig(cmd)
	if err != nil {
		return fmt.Errorf("unable to set config :%w", err)
	}
	logger := slog.Default()
	client, err := k8s.New(ctx, &k8s.Options{
		Config: cfg,
		Logger: logger,
	})
	if err != nil {
		return fmt.Errorf("unable to create Kubernetes client: %w", err)
	}
	svr := server.New(client)
	svrCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		errCh <- svr.ListenAndServe()
	}()

	select {
	case <-svrCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if shutdownErr := svr.Shutdown(shutdownCtx); shutdownErr != nil {
			logger.Error("server shutdown error", "error", shutdownErr)
		}
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}

	return nil
}
