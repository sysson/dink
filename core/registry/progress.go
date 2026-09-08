package registry

import (
	"context"
	"errors"
	"io"
	"syscall"

	"github.com/sysson/syskit/logx"
	"github.com/sysson/syskit/stream"
)

// writeDistributionProgress forwards progress events to the client until the
// channel is closed, cancelling the operation if the client stops reading.
func writeDistributionProgress(ctx context.Context, cancel context.CancelFunc, outStream io.Writer, progressChan <-chan stream.Progress) {
	progressOutput := stream.NewJSONProgressOutput(outStream, false)
	operationCancelled := false

	for prog := range progressChan {
		if err := progressOutput.WriteProgress(prog); err != nil && !operationCancelled {
			if errors.Is(err, syscall.EPIPE) {
				logx.G(ctx).Info("Pull session cancelled")
			} else {
				logx.G(ctx).Error("error writing progress to client: %v", err)
			}
			cancel()
			operationCancelled = true
		}
	}
}
