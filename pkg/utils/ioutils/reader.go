package ioutils

import (
	"context"
	"io"
	"sync/atomic"
)

type readCloserWrapper struct {
	io.Reader
	closer func() error
	closed atomic.Bool
}

func (r *readCloserWrapper) Close() error {
	if !r.closed.CompareAndSwap(false, true) {
		return nil
	}
	return r.closer()
}

func NewReadCloserWrapper(r io.Reader, closer func() error) io.ReadCloser {
	return &readCloserWrapper{
		Reader: r,
		closer: closer,
	}
}

type cancelReadCloser struct {
	cancel func()
	pR     *io.PipeReader
	pW     *io.PipeWriter
	closed atomic.Bool
}

func NewCancelReadCloser(ctx context.Context, in io.ReadCloser) io.ReadCloser {
	pR, pW := io.Pipe()

	doneCtx, cancel := context.WithCancel(ctx)

	p := &cancelReadCloser{
		cancel: cancel,
		pR:     pR,
		pW:     pW,
	}

	go func() {
		_, err := io.Copy(pW, in)
		select {
		case <-ctx.Done():
		default:
			p.closeWithError(err)
		}
		_ = in.Close()
	}()
	go func() {
		for {
			select {
			case <-ctx.Done():
				p.closeWithError(ctx.Err())
			case <-doneCtx.Done():
				return
			}
		}
	}()

	return p
}

func (p *cancelReadCloser) Read(buf []byte) (int, error) {
	return p.pR.Read(buf)
}

func (p *cancelReadCloser) closeWithError(err error) {
	_ = p.pW.CloseWithError(err)
	p.cancel()
}

func (p *cancelReadCloser) Close() error {
	if !p.closed.CompareAndSwap(false, true) {
		return nil
	}
	p.closeWithError(io.EOF)
	return nil
}
