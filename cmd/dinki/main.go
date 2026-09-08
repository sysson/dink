package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sysson/dink/cmd/dinki/command"
)

func main() {
	ctx := context.Background()

	stdOut := os.Stdout
	stdErr := os.Stderr

	r, err := command.New(stdOut, stdErr)
	if err != nil {
		_, _ = fmt.Fprintf(stdErr, "error: %v\n", err)
		os.Exit(1)
	}

	if err := r.Run(ctx, os.Args[0:]); err != nil {
		_, _ = fmt.Fprintf(stdErr, "error: %v\n", err)
		os.Exit(1)
	}

	os.Exit(0)
}
