package dink

import (
	"context"
	"fmt"

	"github.com/sysson/dink/cmd/version"
	"github.com/sysson/dink/dink/command"
	"github.com/sysson/dink/internal/config"
	"github.com/urfave/cli/v3"
)

func RootCmd() *cli.Command {
	return &cli.Command{
		Name:    "dink",
		Usage:   "A Kubernetes API compatibility proxy",
		Version: version.Version,
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

	d := command.New(cfg)
	return d.Start(ctx)
}
