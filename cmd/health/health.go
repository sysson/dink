package health

import (
	"context"
	"fmt"

	"github.com/sysson/dink/cmd/version"
	"github.com/urfave/cli/v3"
)

func Cmd() *cli.Command {
	return &cli.Command{
		Name:    "health",
		Usage:   "Show Health Status",
		Version: version.GetVersion(),
		Action:  healthAction,
	}
}

func healthAction(_ context.Context, _ *cli.Command) error {
	fmt.Println("OK")
	return nil
}
