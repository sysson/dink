package root

import (
	"context"

	"github.com/sysson/dink/cmd/version"
	"github.com/sysson/dink/pkg/dink"
	"github.com/urfave/cli/v3"
)

func Cmd() *cli.Command {
	return &cli.Command{
		Name:    "dink",
		Usage:   "A Kubernetes API compatibility proxy",
		Version: version.GetVersion(),
		Action:  dinkAction,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "config",
				Value: "",
				Usage: "Path to the configuration file",
			},
		},
	}
}

func dinkAction(ctx context.Context, cmd *cli.Command) error {
	svr, err := dink.Init(ctx, cmd)
	if err != nil {
		return err
	}
	return svr.Run(ctx)
}
