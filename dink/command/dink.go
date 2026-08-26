package command

import (
	"context"
	"io"
	"log/slog"
	"os"

	"github.com/go-chi/httplog/v3"
	"github.com/sysson/dink/dink/pkg/config"
	"github.com/sysson/dink/dink/pkg/version"
	"github.com/urfave/cli/v3"
)

type Runner interface {
	Run(ctx context.Context) error
}

type runner struct {
	*cli.Command
}

func New(stdout, stderr io.Writer) (Runner, error) {
	v := version.Get()

	logFormat := httplog.SchemaOTEL.Concise(version.IsDev())
	setDefaultLogger(stderr, slog.LevelInfo, v.Version, logFormat.ReplaceAttr)

	cmd := runnerCmd(stdout, stderr)
	return &runner{
		Command: cmd,
	}, nil
}

func (r *runner) Run(ctx context.Context) error {
	return r.Command.Run(ctx, os.Args)
}

func runnerCmd(stdout, stderr io.Writer) *cli.Command {
	cfg := config.New()
	opts := newOptions(cfg)

	cmd := cli.Command{
		Name:      "dink",
		Usage:     "A Kubernetes API compatibility proxy",
		Version:   version.Get().Version,
		Writer:    stdout,
		ErrWriter: stderr,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			opts.flags = &cmd.Flags
			cli, err := newCLI(opts)
			if err != nil {
				return err
			}
			cli.stdErr = stderr
			cli.stdOut = stdout
			version.SetAPI(cli.cfg.APIVersion, cli.cfg.MinAPIVersion)
			logFormat := httplog.SchemaOTEL.Concise(version.IsDev())
			setDefaultLogger(stderr, getLogLevel(cli.cfg.LogLevel), version.Get().Version, logFormat.ReplaceAttr)
			return run(ctx, cli)
		},
	}
	opts.addFlags(&cmd.Flags)
	return &cmd
}

func setDefaultLogger(out io.Writer, level slog.Level, version string, replaceAttr func(groups []string, a slog.Attr) slog.Attr) {
	logger := slog.New(slog.NewJSONHandler(
		out, &slog.HandlerOptions{Level: level, ReplaceAttr: replaceAttr},
	)).With(
		slog.String("version", version),
	)
	slog.SetDefault(logger)
}

func run(ctx context.Context, cli *dinkCLI) error {
	return cli.start(ctx)
}
