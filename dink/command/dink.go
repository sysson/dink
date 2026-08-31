package command

import (
	"context"
	"io"
	"log/slog"

	"github.com/go-chi/httplog/v3"
	"github.com/sysson/dink/dink/config"
	"github.com/sysson/dink/dink/version"
	"github.com/urfave/cli/v3"
)

type Runner interface {
	Run(ctx context.Context, args []string) error
}

type runner struct {
	*cli.Command
}

func New(stdout, stderr io.Writer) (Runner, error) {
	v := version.Get()

	logFormat := httplog.SchemaOTEL.Concise(version.IsDev())
	logLevel := new(slog.LevelVar)
	logger := slog.New(slog.NewJSONHandler(
		stderr, &slog.HandlerOptions{Level: logLevel, ReplaceAttr: logFormat.ReplaceAttr},
	)).With(
		slog.String("version", v.Version),
	)
	slog.SetDefault(logger)

	cmd := runnerCmd(stdout, stderr, logLevel)
	return &runner{
		Command: cmd,
	}, nil
}

func runnerCmd(stdout, stderr io.Writer, leveler *slog.LevelVar) *cli.Command {
	opts := newOptions(config.Default(), new(config.Config))

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
			_ = leveler.UnmarshalText([]byte(cli.cfg.Log.Level))
			err = run(ctx, cli)
			leveler.Set(slog.LevelInfo)
			return err
		},
	}
	opts.addFlags(&cmd.Flags)
	return &cmd
}

func run(ctx context.Context, cli *dinkCLI) error {
	return cli.start(ctx)
}
