package main

import (
	"context"
	"log"
	"os"

	"github.com/sysson/dink/cmd/dink"
)

func main() {
	if err := dink.RootCmd().Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}
