package version

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/urfave/cli/v3"
)

var (
	version = "Dev"
	commit  = "None"
	date    = "Unknown"
)

func Cmd() *cli.Command {
	bs, ok := debug.ReadBuildInfo()
	if ok {
		if bs.Main.Version != "(devel)" {
			version = bs.Main.Version
		}
		for _, setting := range bs.Settings {
			switch setting.Key {
			case "vcs.revision":
				commit = setting.Value
			case "vcs.time":
				date = setting.Value
			}
		}
	}
	return &cli.Command{
		Name:    "version",
		Usage:   "Show the version information",
		Version: version,
		Action:  versionAction,
	}
}

func versionAction(_ context.Context, _ *cli.Command) error {
	fmt.Println("Version:", version)
	fmt.Println("Commit:", commit)
	fmt.Println("Date:", date)
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
