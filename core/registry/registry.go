package registry

import (
	"context"
	"crypto/tls"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/docker/oci"
	"github.com/docker/oci/ociauth"
	"github.com/docker/oci/ociclient"
	"github.com/sysson/dink/core/types"
)

// Authenticate verifies auth against host by using it to make a request,
// letting [ociauth.NewStdTransport] negotiate whatever Basic or Bearer
// challenge the registry responds with (the same transport pulls use). It
// does not persist anything: the docker client that sent auth owns the
// credentials and resends them via X-Registry-Auth on later requests.
func Authenticate(ctx context.Context, auth types.RegistryAuth) (string, error) {
	host, insecure, err := registryHost(auth.ServerAddress)
	if err != nil {
		return "", err
	}
	cfg, err := ociauth.Load(nil)
	if err != nil {
		return "", err
	}
	transport := ociauth.NewStdTransport(ociauth.StdTransportParams{
		Config:    authConfigSource{host: host, auth: &auth, fallback: cfg},
		Transport: RegistryTransport(nil, nil),
	})

	scheme := "https"
	if insecure {
		scheme = "http"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+host+"/v2/", nil)
	if err != nil {
		return "", err
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return "", fmt.Errorf("contacting registry %s: %w", host, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("registry %s rejected the supplied credentials", host)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("registry %s returned %s", host, resp.Status)
	}
	return "", nil
}

type RegistryService struct {
	// internal is the client for dink's own registry. Its credentials are
	// dink's and never change, so one client is shared for the process.
	internal oci.Interface
	// internalErr is why that client could not be built. It is reported
	// only to the endpoints that need the registry; the rest still work.
	internalErr error
}

// New returns an image service backed by internal.
func New(internal oci.Interface) *RegistryService {
	if internal == nil {
		return Unavailable(fmt.Errorf("registry client is not set"))
	}
	return &RegistryService{internal: internal}
}

// Unavailable returns an image service whose registry-backed endpoints report
// err while endpoints that do not need the registry can still be mounted.
func Unavailable(err error) *RegistryService {
	return &RegistryService{internalErr: err}
}

// client returns the internal registry client, or the error that prevented
// it from being built.
func (r *RegistryService) client() (oci.Interface, error) {
	if r.internalErr != nil {
		return nil, r.internalErr
	}
	return r.internal, nil
}

func RegistryTransport(cfg *tls.Config, headers map[string][]string) http.RoundTripper {
	direct := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	base := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         direct.DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig:     cfg,
		IdleConnTimeout:     30 * time.Second,
	}
	if headers == nil {
		headers = map[string][]string{}
	}
	headers["User-Agent"] = []string{"dink"}
	return &registryRoundTripper{
		base:    base,
		headers: headers,
	}
}

type registryRoundTripper struct {
	base    http.RoundTripper
	headers map[string][]string
}

func (h *registryRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	maps.Copy(req.Header, h.headers)
	return h.base.RoundTrip(req)
}

// ClientOptions configures an OCI registry client.
type ClientOptions struct {
	Auth      *types.RegistryAuth
	Transport http.RoundTripper
}

// NewClient returns a client for a registry host or URL, using auth when given
// and falling back to the local docker config otherwise. Building the client
// does not contact the registry.
func NewClient(host string, opts ClientOptions) (oci.Interface, error) {
	host, insecure, err := registryHost(host)
	if err != nil {
		return nil, err
	}
	cfg, err := ociauth.Load(nil)
	if err != nil {
		return nil, err
	}
	transport := ociauth.NewStdTransport(ociauth.StdTransportParams{
		Config:    authConfigSource{host: host, auth: opts.Auth, fallback: cfg},
		Transport: opts.Transport,
	})
	return ociclient.New(host, &ociclient.Options{
		Transport: transport,
		Insecure:  insecure,
	})
}

func registryHost(value string) (string, bool, error) {
	if value == "" {
		return "docker.io", false, nil
	}
	if !strings.Contains(value, "://") {
		return value, isLoopback(value), nil
	}
	u, err := url.Parse(value)
	if err != nil {
		return "", false, err
	}
	return u.Host, u.Scheme == "http", nil
}

// isLoopback reports whether host addresses the local machine, which docker
// treats as an insecure registry reachable over plain HTTP.
func isLoopback(host string) bool {
	hostname, _, err := net.SplitHostPort(host)
	if err != nil {
		hostname = host
	}
	if hostname == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(hostname, "[]"))
	return ip != nil && ip.IsLoopback()
}

// authConfigSource is an [ociauth.Config] that returns the credentials the
// docker client supplied for a pull for the matching host, falling back to
// the local docker config for other hosts (e.g. when a pull crosses
// registries).
type authConfigSource struct {
	host     string
	auth     *types.RegistryAuth
	fallback ociauth.Config
}

func (s authConfigSource) EntryForRegistry(host string) (ociauth.ConfigEntry, error) {
	if s.auth != nil && host == s.host {
		return ociauth.ConfigEntry{
			RefreshToken: s.auth.RefreshToken,
			AccessToken:  s.auth.AccessToken,
			Username:     s.auth.Username,
			Password:     s.auth.Password,
		}, nil
	}
	if s.fallback != nil {
		return s.fallback.EntryForRegistry(host)
	}
	return ociauth.ConfigEntry{}, nil
}
