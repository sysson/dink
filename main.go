package main

import (
	"context"
	"log"
	"os"

	"github.com/sysson/dink/cmd/health"
	"github.com/sysson/dink/cmd/root"
	"github.com/sysson/dink/cmd/version"
)

func main() {
	cmd := root.Cmd()

	cmd.Commands = append(cmd.Commands, version.Cmd())
	cmd.Commands = append(cmd.Commands, health.Cmd())

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}
