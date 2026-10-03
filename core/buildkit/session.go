package buildkit

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	control "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/session/grpchijack"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/registry/api"
	"github.com/sysson/syskit/httpx"
	"github.com/sysson/syskit/logx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const sessionIDHeader = "X-Docker-Expose-Session-Uuid"
const sessionMethodHeader = "X-Docker-Expose-Session-Grpc-Method"

type bridge struct {
	id         identity.Identity
	ctx        context.Context
	cancel     context.CancelFunc
	caller     session.Caller
	ready      chan struct{}
	solving    atomic.Bool
	mu         sync.RWMutex
	credential *api.BuildCredential
}

func (b *bridge) setCredential(credential *api.BuildCredential) {
	b.mu.Lock()
	b.credential = credential
	b.mu.Unlock()
}

func (g *Gateway) getBridge(ctx context.Context, sessionID string, id identity.Identity) (*bridge, error) {
	// The session may arrive concurrently with Solve.
	timer := time.NewTicker(10 * time.Millisecond)
	defer timer.Stop()
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		g.mu.Lock()
		b := g.sessions[sessionID]
		g.mu.Unlock()
		if b != nil {
			if b.id != id {
				return nil, status.Error(codes.PermissionDenied, "session belongs to another identity")
			}
			select {
			case <-b.ready:
				if b.ctx.Err() != nil {
					return nil, status.Error(codes.Unavailable, "build session disconnected")
				}
				return b, nil
			case <-waitCtx.Done():
				return nil, status.Error(codes.Unavailable, "build session is not ready")
			}
		}
		select {
		case <-waitCtx.Done():
			return nil, status.Error(codes.Unavailable, "build session is not connected")
		case <-timer.C:
		}
	}
}

func (g *Gateway) Session(server control.Control_SessionServer) error {
	if _, err := g.check(server.Context()); err != nil {
		return err
	}
	conn, _, headers := grpchijack.Hijack(server)
	return g.handleSession(server.Context(), conn, http.Header(headers))
}

func (g *Gateway) HandleHTTPRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	if _, err := g.check(ctx); err != nil {
		return httpx.NewHTTPError(http.StatusServiceUnavailable, err)
	}
	if r.Header.Get(sessionIDHeader) == "" {
		return httpx.BadRequest(errors.New("session UUID is required"))
	}
	conn, err := Hijack(w, r)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err := g.handleSession(ctx, conn, r.Header); err != nil {
		// The upgraded connection owns the response; HTTP error handlers cannot write to it.
		if errors.Is(err, context.Canceled) {
			logx.G(ctx).Info("BuildKit session disconnected", "session", r.Header.Get(sessionIDHeader), "error", err)
		} else {
			logx.G(ctx).Error("BuildKit session failed", "session", r.Header.Get(sessionIDHeader), "error", err)
		}
	}
	return nil
}

func (g *Gateway) handleSession(ctx context.Context, conn net.Conn, headers http.Header) error {
	headers = canonicalHeaders(headers)
	id, err := g.check(ctx)
	if err != nil {
		return err
	}
	sessionID := headers.Get(sessionIDHeader)
	if sessionID == "" || strings.Contains(sessionID, ":") {
		return status.Error(codes.InvalidArgument, "a valid session UUID is required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b := &bridge{id: id, ctx: ctx, cancel: cancel, ready: make(chan struct{})}
	g.mu.Lock()
	if _, exists := g.sessions[sessionID]; exists {
		g.mu.Unlock()
		return status.Error(codes.AlreadyExists, "session UUID is already connected")
	}
	g.sessions[sessionID] = b
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.sessions, sessionID)
		g.mu.Unlock()
	}()
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- g.manager.HandleConn(ctx, conn, headers)
		cancel()
	}()
	readyCtx, readyCancel := context.WithTimeout(ctx, 10*time.Second)
	defer readyCancel()
	b.caller, err = g.manager.Get(readyCtx, sessionID, false)
	if err != nil {
		return err
	}
	upstream, err := session.NewSession(ctx, tenantRef(id, b.caller.SharedKey()))
	if err != nil {
		return err
	}
	defer func() { _ = upstream.Close() }()
	upstream.Allow(&sessionProxy{caller: b.caller, methods: headers.Values(sessionMethodHeader)})
	upstream.Allow(&authProxy{bridge: b, host: g.registry.Host})
	upstreamDone := make(chan error, 1)
	go func() {
		upstreamDone <- upstream.Run(ctx, func(ctx context.Context, protocol string, meta map[string][]string) (net.Conn, error) {
			meta[sessionIDHeader] = []string{sessionID}
			c, err := grpchijack.Dialer(g.control)(ctx, protocol, meta)
			if err == nil {
				close(b.ready)
			}
			return c, err
		})
		cancel()
	}()
	select {
	case err := <-clientDone:
		return err
	case err := <-upstreamDone:
		return err
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

type sessionProxy struct {
	caller  session.Caller
	methods []string
}

func (p *sessionProxy) Register(server *grpc.Server) {
	services := map[string]*grpc.ServiceDesc{}
	seen := map[string]bool{}
	for _, method := range p.methods {
		service, name, ok := strings.Cut(strings.TrimPrefix(method, "/"), "/")
		if !ok || name == "" || strings.Contains(name, "/") || seen[method] || service == "moby.filesync.v1.Auth" || service == "grpc.health.v1.Health" {
			continue
		}
		seen[method] = true
		desc := services[service]
		if desc == nil {
			desc = &grpc.ServiceDesc{ServiceName: service, HandlerType: (*any)(nil)}
			services[service] = desc
		}
		fullMethod := "/" + service + "/" + name
		desc.Streams = append(desc.Streams, grpc.StreamDesc{
			StreamName: name, ClientStreams: true, ServerStreams: true,
			Handler: func(_ any, stream grpc.ServerStream) error {
				return forwardRPC(outgoingContext(stream.Context()), p.caller.Conn(), fullMethod, nil, stream)
			},
		})
	}
	for _, desc := range services {
		server.RegisterService(desc, p)
	}
}

type authProxy struct {
	auth.UnimplementedAuthServer
	bridge *bridge
	host   string
}

func (p *authProxy) Register(server *grpc.Server) {
	auth.RegisterAuthServer(server, p)
}

func (p *authProxy) Credentials(ctx context.Context, request *auth.CredentialsRequest) (*auth.CredentialsResponse, error) {
	if request.Host == p.host {
		p.bridge.mu.RLock()
		defer p.bridge.mu.RUnlock()
		if p.bridge.credential == nil {
			return nil, status.Error(codes.PermissionDenied, "no registry credential for this build")
		}
		return &auth.CredentialsResponse{Username: p.bridge.credential.Username, Secret: p.bridge.credential.Password}, nil
	}
	return auth.NewAuthClient(p.bridge.caller.Conn()).Credentials(ctx, request)
}

func (p *authProxy) FetchToken(ctx context.Context, request *auth.FetchTokenRequest) (*auth.FetchTokenResponse, error) {
	if request.Host == p.host {
		return nil, status.Error(codes.Unimplemented, "Dinki uses Basic authentication")
	}
	return auth.NewAuthClient(p.bridge.caller.Conn()).FetchToken(ctx, request)
}

func (p *authProxy) GetTokenAuthority(ctx context.Context, request *auth.GetTokenAuthorityRequest) (*auth.GetTokenAuthorityResponse, error) {
	if request.Host == p.host {
		return nil, status.Error(codes.Unimplemented, "Dinki uses Basic authentication")
	}
	return auth.NewAuthClient(p.bridge.caller.Conn()).GetTokenAuthority(ctx, request)
}

func (p *authProxy) VerifyTokenAuthority(ctx context.Context, request *auth.VerifyTokenAuthorityRequest) (*auth.VerifyTokenAuthorityResponse, error) {
	if request.Host == p.host {
		return nil, status.Error(codes.Unimplemented, "Dinki uses Basic authentication")
	}
	return auth.NewAuthClient(p.bridge.caller.Conn()).VerifyTokenAuthority(ctx, request)
}

func canonicalHeaders(headers http.Header) http.Header {
	canonical := make(http.Header)
	for key, values := range headers {
		canonical[http.CanonicalHeaderKey(key)] = values
	}
	return canonical
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(data []byte) (int, error) { return c.reader.Read(data) }

func Hijack(w http.ResponseWriter, r *http.Request) (net.Conn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "h2c") {
		return nil, httpx.BadRequest(errors.New("upgrade: h2c is required"))
	}
	conn, buffered, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return nil, fmt.Errorf("hijacking BuildKit connection: %w", err)
	}
	if _, err := buffered.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: h2c\r\n\r\n"); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := buffered.Flush(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &bufferedConn{Conn: conn, reader: buffered.Reader}, nil
}
