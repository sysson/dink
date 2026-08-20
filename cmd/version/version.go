package version

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"
)

var (
	version = "Dev"
	commit  = "None"
	date    = "Unknown"
)

func Cmd() *cli.Command {
	return &cli.Command{
		Name:    "version",
		Usage:   "Show the version information",
		Version: version,
		Action:  versionAction,
	}
}

func versionAction(_ context.Context, _ *cli.Command) error {
	fmt.Println("Version:", version)
	return nil
}

func IsDev() bool {
	return version == "Dev"
}

func GetVersion() string {
	return version
}

func GetCommit() string {
	return commit
}

func GetDate() string {
	return date
}
