package registry

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"

	"github.com/containerd/platforms"
	"github.com/docker/oci"
	"github.com/docker/oci/ociauth"
	"github.com/docker/oci/ociclient"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/pkg/types"
)

type ImageService struct {
	// internal is the client for dink's own registry. Its credentials are
	// dink's and never change, so one client is shared for the process.
	internal oci.Interface
	// internalErr is why that client could not be built. It is reported
	// only to the endpoints that need the registry; the rest still work.
	internalErr error
}

// New returns an image service backed by internal.
func New(internal oci.Interface) *ImageService {
	if internal == nil {
		return Unavailable(fmt.Errorf("registry client is not set"))
	}
	return &ImageService{internal: internal}
}

// Unavailable returns an image service whose registry-backed endpoints report
// err while endpoints that do not need the registry can still be mounted.
func Unavailable(err error) *ImageService {
	return &ImageService{internalErr: err}
}

// client returns the internal registry client, or the error that prevented
// it from being built.
func (s *ImageService) client() (oci.Interface, error) {
	if s.internalErr != nil {
		return nil, s.internalErr
	}
	return s.internal, nil
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
	if !strings.Contains(value, "://") {
		return value, isLoopback(value), nil
	}
	u, err := url.Parse(value)
	if err != nil {
		return "", false, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false, fmt.Errorf("unsupported registry scheme %q", u.Scheme)
	}
	if u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", false, fmt.Errorf("registry URL must be %s://host:port", u.Scheme)
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

// platformMatcher returns a matcher for the first requested platform, or nil
// to use the runtime default.
func platformMatcher(pls []ocispec.Platform) platforms.MatchComparer {
	if len(pls) == 0 {
		return nil
	}
	return platforms.Only(pls[0])
}

// ociPlatformToSpec converts an oci.Platform to the OCI image-spec platform
// used by platform matchers.
func ociPlatformToSpec(p oci.Platform) ocispec.Platform {
	return ocispec.Platform{
		Architecture: p.Architecture,
		OS:           p.OS,
		OSVersion:    p.OSVersion,
		OSFeatures:   p.OSFeatures,
		Variant:      p.Variant,
	}
}

func matcherString(m platforms.MatchComparer) string {
	if m == nil {
		return runtime.GOOS + "/" + runtime.GOARCH
	}
	return fmt.Sprintf("%v", m)
}
