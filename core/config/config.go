package config

import (
	"crypto/tls"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/sysson/dink/core/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
)

type Log struct {
	Level string `json:"level,omitempty"`
}

type AccessLog struct {
	Level   string `json:"level,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
}

type Kubernetes struct {
	SystemNamespace  string           `json:"systemNamespace,omitempty"`
	DefaultNamespace string           `json:"defaultNamespace,omitempty"`
	HealthPort       string           `json:"healthPort,omitempty"`
	DefaultResources ResourceDefaults `json:"defaultResources"`
	NodePlacement    NodePlacement    `json:"nodePlacement"`
}

// NodePlacement confines tenant workloads to nodes that are either unlabelled
// or labelled with the tenant's own namespace, so cluster operators can
// dedicate nodes to a tenant.
type NodePlacement struct {
	Enabled *bool `json:"enabled,omitempty"`
	// LabelKey is the node label whose value names the owning tenant namespace.
	LabelKey string `json:"labelKey,omitempty"`
	// Tolerate adds a toleration for LabelKey=<namespace>:NoSchedule so operators
	// can taint dedicated nodes and keep unrelated workloads off them.
	Tolerate *bool `json:"tolerate,omitempty"`
}

func (n NodePlacement) IsEnabled() bool {
	return n.Enabled != nil && *n.Enabled
}

func (n NodePlacement) TolerationsEnabled() bool {
	return n.Tolerate == nil || *n.Tolerate
}

func (n NodePlacement) Validate() error {
	if !n.IsEnabled() {
		return nil
	}
	if n.LabelKey == "" {
		return errors.New("nodePlacement.labelKey must not be empty when nodePlacement is enabled")
	}
	if errs := validation.IsQualifiedName(n.LabelKey); len(errs) > 0 {
		return fmt.Errorf("nodePlacement.labelKey %q is not a valid label key: %s", n.LabelKey, strings.Join(errs, "; "))
	}
	return nil
}

type ResourceValues struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

type ResourceDefaults struct {
	Limits   ResourceValues `json:"limits"`
	Requests ResourceValues `json:"requests"`
}

func (defaults ResourceDefaults) Validate() error {
	var errs []error
	for _, entry := range []struct {
		name     string
		value    string
		resource corev1.ResourceName
	}{
		{"limits.cpu", defaults.Limits.CPU, corev1.ResourceCPU},
		{"limits.memory", defaults.Limits.Memory, corev1.ResourceMemory},
		{"requests.cpu", defaults.Requests.CPU, corev1.ResourceCPU},
		{"requests.memory", defaults.Requests.Memory, corev1.ResourceMemory},
	} {
		if entry.value == "" {
			continue
		}
		quantity, err := resource.ParseQuantity(entry.value)
		if err != nil || quantity.Sign() <= 0 {
			errs = append(errs, fmt.Errorf("%s must be a positive Kubernetes quantity", entry.name))
			continue
		}
		if entry.resource == corev1.ResourceCPU {
			if quantity.Cmp(*resource.NewMilliQuantity(math.MaxInt64/1_000_000, resource.DecimalSI)) > 0 ||
				quantity.Cmp(*resource.NewMilliQuantity(quantity.MilliValue(), resource.DecimalSI)) != 0 {
				errs = append(errs, fmt.Errorf("%s must fit Docker NanoCPUs in whole millicores", entry.name))
			}
		} else if quantity.Cmp(*resource.NewQuantity(math.MaxInt64, resource.BinarySI)) > 0 ||
			quantity.Cmp(*resource.NewQuantity(quantity.Value(), resource.BinarySI)) != 0 {
			errs = append(errs, fmt.Errorf("%s must fit Docker memory in whole bytes", entry.name))
		}
	}
	for _, pair := range []struct{ name, request, limit string }{
		{"cpu", defaults.Requests.CPU, defaults.Limits.CPU},
		{"memory", defaults.Requests.Memory, defaults.Limits.Memory},
	} {
		if pair.request == "" || pair.limit == "" {
			continue
		}
		request, reqErr := resource.ParseQuantity(pair.request)
		limit, limErr := resource.ParseQuantity(pair.limit)
		if reqErr == nil && limErr == nil && request.Cmp(limit) > 0 {
			errs = append(errs, fmt.Errorf("%s request exceeds limit", pair.name))
		}
	}
	return errors.Join(errs...)
}

type TLS struct {
	CertFile      string `json:"certFile,omitempty"`
	KeyFile       string `json:"keyFile,omitempty"`
	ClientCAFile  string `json:"clientCAFile,omitempty"`
	MinTLSVersion string `json:"minTLSVersion,omitempty"`
}

type Server struct {
	Host                  string `json:"host,omitempty"`
	Port                  string `json:"port,omitempty"`
	DisableTLS            *bool  `json:"disableTLS,omitempty"`
	AllowPlaintextWithTLS *bool  `json:"allowPlaintextWithTLS,omitempty"`
	TLSPort               string `json:"tlsPort,omitempty"`
}

type Auth struct {
	Plugins   []AuthPlugin `json:"plugins,omitempty"`
	PluginDir string       `json:"pluginDir,omitempty"`
}

type BuildKit struct {
	URL         string `json:"url,omitempty"`
	CAFile      string `json:"caFile,omitempty"`
	CertFile    string `json:"certFile,omitempty"`
	KeyFile     string `json:"keyFile,omitempty"`
	ServerName  string `json:"serverName,omitempty"`
	RegistryURL string `json:"registryURL,omitempty"`
}

// Registry is how dink reaches dinki's internal RegistryService API. dink
// makes no OCI requests itself. CAFile defaults to TLS.ClientCAFile and the
// client key pair defaults to TLS.CertFile/KeyFile. PullHost is the registry
// name used in tenant image references (dinki serve-node's registryHost).
type Registry struct {
	URL      string `json:"url,omitempty"`
	CAFile   string `json:"caFile,omitempty"`
	CertFile string `json:"certFile,omitempty"`
	KeyFile  string `json:"keyFile,omitempty"`
	PullHost string `json:"pullHost,omitempty"`
}

type Config struct {
	Log        Log        `json:"log"`
	AccessLog  AccessLog  `json:"accessLog"`
	Kubernetes Kubernetes `json:"kubernetes"`
	TLS        TLS        `json:"tls"`
	Server     Server     `json:"server"`
	BuildKit   BuildKit   `json:"buildKit"`
	Auth       Auth       `json:"auth"`
	Registry   Registry   `json:"registry"`
}

type AuthPlugin struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func Default() *Config {
	return &Config{
		Log: Log{
			Level: "info",
		},
		AccessLog: AccessLog{
			Level:   "error",
			Enabled: new(true),
		},
		Kubernetes: Kubernetes{
			SystemNamespace:  types.DefaultSystemNamespace,
			DefaultNamespace: types.DefaultNamespace,
			HealthPort:       "8080",
			DefaultResources: ResourceDefaults{
				Limits:   ResourceValues{CPU: "500m", Memory: "512Mi"},
				Requests: ResourceValues{CPU: "100m", Memory: "128Mi"},
			},
			NodePlacement: NodePlacement{
				Enabled:  new(false),
				LabelKey: "dink.io/tenant",
				Tolerate: new(true),
			},
		},
		TLS: TLS{
			CertFile:      "/etc/dink/certs/server.crt",
			KeyFile:       "/etc/dink/certs/server.key",
			ClientCAFile:  "/etc/dink/certs/ca.crt",
			MinTLSVersion: "1.3",
		},
		Server: Server{
			Host:                  "",
			Port:                  "2375",
			DisableTLS:            new(false),
			AllowPlaintextWithTLS: new(false),
			TLSPort:               "2376",
		},
		Auth: Auth{
			Plugins:   []AuthPlugin{},
			PluginDir: "/var/lib/dink/plugins",
		},
		Registry: Registry{
			URL:      "https://dinki.dink-system.svc.cluster.local:5000",
			PullHost: "dinki.io",
		},
	}
}

func (l *Log) Validate() error {
	return validateLogLevel("logLevel", l.Level)
}

func (a *AccessLog) Validate() error {
	return validateLogLevel("accessLogLevel", a.Level)
}

func (k *Kubernetes) Validate() error {
	var errs []error
	if k.SystemNamespace == "" {
		errs = append(errs, errors.New("systemNamespace must not be empty"))
	}
	if k.DefaultNamespace == "" {
		errs = append(errs, errors.New("defaultNamespace must not be empty"))
	}
	if err := validatePort("healthPort", k.HealthPort); err != nil {
		errs = append(errs, err)
	}
	if err := k.DefaultResources.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := k.NodePlacement.Validate(); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (s *Server) TLSEnabled() bool {
	return s.DisableTLS == nil || !*s.DisableTLS
}

func (s *Server) PlaintextEnabled() bool {
	return !s.TLSEnabled() || (s.AllowPlaintextWithTLS != nil && *s.AllowPlaintextWithTLS)
}

func (s *Server) Validate() error {
	var errs []error
	if err := validateHost(s.Host); err != nil {
		errs = append(errs, err)
	}

	tlsEnabled := s.TLSEnabled()
	plaintextEnabled := s.PlaintextEnabled()

	if plaintextEnabled {
		if err := validatePort("port", s.Port); err != nil {
			errs = append(errs, err)
		}
	}
	if tlsEnabled {
		if err := validatePort("tlsPort", s.TLSPort); err != nil {
			errs = append(errs, err)
		}
	}
	if tlsEnabled && plaintextEnabled && s.Port != "" && s.Port == s.TLSPort {
		errs = append(errs, errors.New("tlsPort must differ from port when allowPlaintextWithTLS is enabled"))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (t *TLS) Validate() error {
	var errs []error
	if t.CertFile == "" {
		errs = append(errs, errors.New("tlsCertFile must not be empty when TLS is enabled"))
	}
	if t.KeyFile == "" {
		errs = append(errs, errors.New("tlsKeyFile must not be empty when TLS is enabled"))
	}
	if err := validateMinTLSVersion(t.MinTLSVersion); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (a *Auth) Validate() error {
	var errs []error
	for i, plugin := range a.Plugins {
		if plugin.Name == "" {
			errs = append(errs, fmt.Errorf("authPlugins[%d].name must not be empty", i))
		}
		if plugin.Path == "" {
			errs = append(errs, fmt.Errorf("authPlugins[%d].path must not be empty", i))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (r *Registry) Validate() error {
	errs := []error{}
	if err := validateURL(r.URL, "registryURL", "http", "https"); err != nil {
		errs = append(errs, err)
	}
	if (r.CertFile == "") != (r.KeyFile == "") {
		errs = append(errs, errors.New("registryCertFile and registryKeyFile must be set together"))
	}
	host := r.PullHost
	if strings.Contains(host, ":") {
		var err error
		host, _, err = net.SplitHostPort(host)
		if err != nil {
			errs = append(errs, fmt.Errorf("registryPullHost %q must be a host or host:port: %w", r.PullHost, err))
		}
	}
	if host == "" || (net.ParseIP(host) == nil && !hostnameRE.MatchString(host)) {
		errs = append(errs, fmt.Errorf("registryPullHost %q must be a valid host or host:port", r.PullHost))
	}
	return errors.Join(errs...)
}

func (b *BuildKit) Validate() error {
	var errs []error
	if err := validateURL(b.URL, "buildKitURL", "tcp"); err != nil {
		errs = append(errs, err)
	}
	if (b.CertFile == "") != (b.KeyFile == "") {
		errs = append(errs, errors.New("buildKit.certFile and buildKit.keyFile must be set together"))
	}
	if b.RegistryURL != "" {
		if err := validateURL(b.RegistryURL, "buildKit.registryURL", "http", "https"); err != nil {
			errs = append(errs, err)
		}
	}
	if strings.HasPrefix(b.URL, "unix://") && (b.CAFile != "" || b.CertFile != "" || b.ServerName != "") {
		errs = append(errs, errors.New("BuildKit TLS settings require a tcp endpoint"))
	}
	return errors.Join(errs...)
}

func (c *Config) Validate() error {
	errs := []error{}

	if err := c.Log.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := c.AccessLog.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := c.Kubernetes.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := c.Server.Validate(); err != nil {
		errs = append(errs, err)
	}
	if c.Server.TLSEnabled() {
		if err := c.TLS.Validate(); err != nil {
			errs = append(errs, err)
		}
	}
	if err := c.Auth.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := c.BuildKit.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := c.Registry.Validate(); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

func validateLogLevel(field, level string) error {
	switch level {
	case "debug", "info", "warn", "error":
		return nil
	default:
		return fmt.Errorf("%s=%q is invalid; expected one of debug|info|warn|error", field, level)
	}
}

func validateMinTLSVersion(v string) error {
	switch v {
	case "", "1.2", "1.3":
		return nil
	default:
		return fmt.Errorf("minTLSVersion=%q is invalid; expected 1.2 or 1.3", v)
	}
}

var hostnameRE = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)

func validateHost(value string) error {
	if value == "" {
		return nil
	}
	if net.ParseIP(value) != nil {
		return nil
	}
	if len(value) <= 253 && hostnameRE.MatchString(value) {
		return nil
	}
	return fmt.Errorf("host=%q is invalid; expected an IP address or hostname", value)
}

func validatePort(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	p, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("%s=%q is invalid; expected a numeric port", field, value)
	}
	if p < 1024 || p > 65535 {
		return fmt.Errorf("%s=%q is invalid; expected range 1024-65535", field, value)
	}
	return nil
}

func TLSVersionFromString(v string) (uint16, error) {
	switch v {
	case "1.1":
		return tls.VersionTLS11, nil
	case "1.2":
		return tls.VersionTLS12, nil
	case "1.3":
		return tls.VersionTLS13, nil
	default:
		return 0, fmt.Errorf("invalid TLS version: %q", v)
	}
}

func LoadFiles(paths ...string) ([][]byte, error) {
	var files [][]byte
	for _, path := range paths {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		files = append(files, data)
	}
	return files, nil
}

func validateURL(value, name string, networkSchemes ...string) error {
	if value == "" {
		return nil
	}

	u, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("%s=%q is invalid: %w", name, value, err)
	}

	for _, scheme := range networkSchemes {
		if scheme != u.Scheme {
			continue
		}
		host, port, err := net.SplitHostPort(u.Host)
		if err != nil {
			return fmt.Errorf("%s=%q is invalid; expected %s://host:port", name, value, u.Scheme)
		}
		if host == "" {
			return fmt.Errorf("%s=%q is invalid; host must not be empty", name, value)
		}
		if err := validateHost(host); err != nil {
			return fmt.Errorf("%s=%q is invalid; %w", name, value, err)
		}
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("%s=%q is invalid; port must be in range 1-65535", name, value)
		}
		return nil
	}

	if u.Scheme == "unix" {
		if u.Path == "" || u.Path == "/" {
			return fmt.Errorf("%s=%q is invalid; expected unix:///path/to/socket", name, value)
		}
		return nil
	}

	expected := make([]string, len(networkSchemes))
	for i, scheme := range networkSchemes {
		expected[i] = scheme + "://"
	}
	return fmt.Errorf("%s=%q is invalid; expected a %s or unix:// address", name, value, strings.Join(expected, " or "))
}
