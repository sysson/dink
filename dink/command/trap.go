package command

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/sysson/syskit/logx"
)

const forceQuitCount = 3

func trap(ctx context.Context, cleanup func()) {
	c := make(chan os.Signal, forceQuitCount)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		var interruptCount int
		for sig := range c {
			logx.G(ctx).Info("received signal", "signal", sig.String())
			if interruptCount < forceQuitCount {
				interruptCount++
				if interruptCount == 1 {
					go cleanup()
				}
				continue
			}
			logx.G(ctx).Info("forcing shutdown without cleanup", "signals", interruptCount)
			os.Exit(128 + int(sig.(syscall.Signal)))
		}
	}()
}
