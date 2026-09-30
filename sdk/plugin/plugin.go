// Package plugin serves dink plugins. Plugin authors pass their
// implementations in through the type packages, for example:
//
//	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
//	defer stop()
//	err := plugin.Serve(ctx, plugin.Info{Name: "vault", Version: "0.1.0"}, secrets.Plugin(&resolver{}))
package plugin

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	pluginv1 "github.com/sysson/dink/sdk/plugin/v1"
	"github.com/sysson/dink/sdk/plugin/v1/pluginconnect"
)

// APIVersion is the plugin API version this SDK implements.
const APIVersion = 1

const (
	DefaultAddr     = ":8443"
	shutdownTimeout = 10 * time.Second
)

type Type pluginv1.PluginType

const (
	TypeAuth    = Type(pluginv1.PluginType_PLUGIN_TYPE_AUTH)
	TypeSecrets = Type(pluginv1.PluginType_PLUGIN_TYPE_SECRETS)
	TypeVolumes = Type(pluginv1.PluginType_PLUGIN_TYPE_VOLUMES)
)

func (t Type) String() string {
	return pluginv1.PluginType(t).String()
}

type Info struct {
	Name    string
	Version string
}

type Option func(*options)

type service struct {
	typ     Type
	path    string
	handler http.Handler
}

type options struct {
	addr        string
	healthAddr  string
	tlsConfig   *tls.Config
	mtlsDir     string
	mtlsClients []Client
	services    []service
}

// WithAddr sets the listen address, either "host:port" or "unix:///path/to.sock".
func WithAddr(addr string) Option {
	return func(o *options) { o.addr = addr }
}

// WithHealthAddr also serves /livez and /readyz over plaintext on addr, so kubelet probes work under mutual TLS.
func WithHealthAddr(addr string) Option {
	return func(o *options) { o.healthAddr = addr }
}

// WithTLSConfig serves over TLS with a custom configuration. Prefer WithMutualTLS.
func WithTLSConfig(c *tls.Config) Option {
	return func(o *options) { o.tlsConfig = c }
}

// WithMutualTLS serves TLS from dir, laid out like a Kubernetes TLS Secret, and
// only accepts clients named in clients (default DefaultClient). See MutualTLSConfig.
func WithMutualTLS(dir string, clients ...Client) Option {
	return func(o *options) {
		o.mtlsDir = dir
		o.mtlsClients = clients
	}
}

// WithService registers a plugin type's handler. Type packages wrap this; plugin
// authors should use those instead.
func WithService(t Type, path string, h http.Handler) Option {
	return func(o *options) {
		o.services = append(o.services, service{typ: t, path: path, handler: h})
	}
}

// Serve runs the plugin until ctx is cancelled, then shuts down gracefully.
func Serve(ctx context.Context, info Info, opts ...Option) error {
	o := options{addr: DefaultAddr}
	for _, opt := range opts {
		opt(&o)
	}

	mux, err := newMux(info, o.services)
	if err != nil {
		return err
	}

	tlsConfig := o.tlsConfig
	if o.mtlsDir != "" {
		if tlsConfig != nil {
			return errors.New("WithMutualTLS and WithTLSConfig are mutually exclusive")
		}
		if tlsConfig, err = MutualTLSConfig(o.mtlsDir, o.mtlsClients...); err != nil {
			return err
		}
	}

	servers := []*server{{addr: o.addr, handler: mux, tlsConfig: tlsConfig}}
	if o.healthAddr != "" {
		health := http.NewServeMux()
		registerHealth(health)
		servers = append(servers, &server{addr: o.healthAddr, handler: health})
	}
	return serveAll(ctx, servers)
}

type server struct {
	addr      string
	handler   http.Handler
	tlsConfig *tls.Config
}

func serveAll(ctx context.Context, servers []*server) error {
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)

	errc := make(chan error, len(servers))
	var running []*http.Server
	for _, s := range servers {
		ln, err := listen(s.addr)
		if err != nil {
			_ = shutdown(ctx, running)
			return err
		}
		srv := &http.Server{
			Handler:           s.handler,
			TLSConfig:         s.tlsConfig,
			Protocols:         protocols,
			ReadHeaderTimeout: 10 * time.Second,
		}
		running = append(running, srv)
		go func() {
			if s.tlsConfig != nil {
				errc <- srv.ServeTLS(ln, "", "")
			} else {
				errc <- srv.Serve(ln)
			}
		}()
	}

	// A server that stops before shutdown always reports why, so its error wins.
	var err error
	remaining := len(running)
	select {
	case err = <-errc:
		remaining--
	case <-ctx.Done():
	}
	shutdownErr := shutdown(ctx, running)
	for range remaining {
		if serveErr := <-errc; err == nil && !errors.Is(serveErr, http.ErrServerClosed) {
			err = serveErr
		}
	}
	if err != nil {
		return err
	}
	return shutdownErr
}

func shutdown(ctx context.Context, servers []*http.Server) error {
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	var errs []error
	for _, srv := range servers {
		errs = append(errs, srv.Shutdown(shutdownCtx))
	}
	return errors.Join(errs...)
}

// Handler returns the plugin's HTTP handler without serving it, e.g. for tests.
func Handler(info Info, opts ...Option) (http.Handler, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return newMux(info, o.services)
}

func newMux(info Info, services []service) (*http.ServeMux, error) {
	if info.Name == "" {
		return nil, errors.New("plugin name must not be empty")
	}
	if len(services) == 0 {
		return nil, errors.New("plugin must implement at least one plugin type")
	}

	mux := http.NewServeMux()
	resp := &pluginv1.InfoResponse{
		Name:       info.Name,
		Version:    info.Version,
		ApiVersion: APIVersion,
	}
	seen := map[Type]bool{}
	for _, s := range services {
		if seen[s.typ] {
			return nil, fmt.Errorf("plugin type %s registered more than once", s.typ)
		}
		seen[s.typ] = true
		resp.Types = append(resp.Types, pluginv1.PluginType(s.typ))
		mux.Handle(s.path, s.handler)
	}

	mux.Handle(pluginconnect.NewPluginServiceHandler(infoHandler{resp: resp}))
	registerHealth(mux)
	return mux, nil
}

func registerHealth(mux *http.ServeMux) {
	health := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Handle("GET /livez", health)
	mux.Handle("GET /readyz", health)
}

func listen(addr string) (net.Listener, error) {
	path, ok := strings.CutPrefix(addr, "unix://")
	if !ok {
		return net.Listen("tcp", addr)
	}
	// A socket left behind by a previous run would make Listen fail.
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSocket != 0 {
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	return net.Listen("unix", path)
}

type infoHandler struct {
	resp *pluginv1.InfoResponse
}

func (h infoHandler) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return h.resp, nil
}

var _ pluginconnect.PluginServiceHandler = infoHandler{}
