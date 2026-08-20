package dink

import (
	"github.com/sysson/dink/cmd/version"
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
