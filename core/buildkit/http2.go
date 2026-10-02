package buildkit

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
)

// ServeHTTP2 serves an already-upgraded connection until it closes or ctx is cancelled.
func ServeHTTP2(ctx context.Context, conn net.Conn, handler http.Handler) error {
	listener := &connectionListener{closed: make(chan struct{})}
	listener.conn = &closingConn{Conn: conn, listener: listener}
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	server := &http.Server{
		Protocols:   protocols,
		Handler:     handler,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	defer stop()
	defer func() { _ = server.Close() }()
	defer func() { _ = listener.Close() }()
	err := server.Serve(listener)
	if errors.Is(err, net.ErrClosed) || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

type connectionListener struct {
	conn     *closingConn
	accepted atomic.Bool
	once     sync.Once
	closed   chan struct{}
}

func (l *connectionListener) Accept() (net.Conn, error) {
	if l.accepted.CompareAndSwap(false, true) {
		select {
		case <-l.closed:
			return nil, net.ErrClosed
		default:
			return l.conn, nil
		}
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *connectionListener) Close() error   { return l.conn.Close() }
func (l *connectionListener) Addr() net.Addr { return l.conn.LocalAddr() }

type closingConn struct {
	net.Conn
	listener *connectionListener
}

func (c *closingConn) Close() error {
	err := c.Conn.Close()
	c.listener.once.Do(func() { close(c.listener.closed) })
	return err
}
