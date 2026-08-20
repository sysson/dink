package dink

import (
	"github.com/sysson/dink/cmd/health"
	"github.com/sysson/dink/cmd/version"
	"github.com/urfave/cli/v3"
)

func RootCmd() *cli.Command {

	cmd := &cli.Command{
		Name:    "dink",
		Usage:   "A Kubernetes API compatibility proxy",
		Version: version.GetVersion(),
		Action:  dink,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "config",
				Value: "",
				Usage: "Path to the configuration file",
			},
		},
	}
	cmd.Commands = append(cmd.Commands, version.Cmd())
	cmd.Commands = append(cmd.Commands, health.Cmd())
	return cmd
}
