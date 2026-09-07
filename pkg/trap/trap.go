package trap

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/sysson/syskit/logx"
)

const defaultForceQuitCount = 3

var T = trap

func trap(ctx context.Context, forceQuitCount int, cleanup func()) {
	if forceQuitCount < 1 {
		forceQuitCount = defaultForceQuitCount
	}
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
			logx.G(ctx).Info("forcing shutdown without cleanup", "signals", forceQuitCount)
			os.Exit(128 + int(sig.(syscall.Signal)))
		}
	}()
}
