package version

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"
)

var (
	Version = "Dev"     //nolint:gochecknoglobals // overridden at build time
	Commit  = "None"    //nolint:gochecknoglobals // overridden at build time
	Date    = "Unknown" //nolint:gochecknoglobals // overridden at build time
)

func VersionCmd() *cli.Command {
	return &cli.Command{
		Name:    "version",
		Usage:   "Show the version information",
		Version: Version,
		Action:  versionAction,
	}
}

func versionAction(_ context.Context, _ *cli.Command) error {
	fmt.Println("Version:", Version)
	return nil
}

func IsDev() bool {
	return Version == "Dev"
}
