package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"

	"github.com/sysson/dink/pkg/types"
)

type Log struct {
	Level string `json:"level,omitempty"`
}

type AccessLog struct {
	Level   string `json:"level,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
}

type Kubernetes struct {
	SystemNamespace  string `json:"systemNamespace,omitempty"`
	DefaultNamespace string `json:"defaultNamespace,omitempty"`
	HealthPort string `json:"healthPort,omitempty"`
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
	URL string `json:"url,omitempty"`
}

type Config struct {
	Log        Log        `json:"log"`
	AccessLog  AccessLog  `json:"accessLog"`
	Kubernetes Kubernetes `json:"kubernetes"`
	TLS        TLS        `json:"tls"`
	Server     Server     `json:"server"`
	BuildKit   BuildKit   `json:"buildKit"`
	Auth       Auth       `json:"auth"`
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
			HealthPort: "8080",
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

func (b *BuildKit) Validate() error {
	if b.URL == "" {
		return nil
	}

	u, err := url.Parse(b.URL)
	if err != nil {
		return fmt.Errorf("buildKitURL=%q is invalid: %w", b.URL, err)
	}

	switch u.Scheme {
	case "tcp":
		host, port, err := net.SplitHostPort(u.Host)
		if err != nil {
			return fmt.Errorf("buildKitURL=%q is invalid; expected tcp://host:port", b.URL)
		}
		if host == "" {
			return fmt.Errorf("buildKitURL=%q is invalid; host must not be empty", b.URL)
		}
		if err := validateHost(host); err != nil {
			return fmt.Errorf("buildKitURL=%q is invalid; %w", b.URL, err)
		}
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("buildKitURL=%q is invalid; port must be in range 1-65535", b.URL)
		}
	case "unix":
		if u.Path == "" || u.Path == "/" {
			return fmt.Errorf("buildKitURL=%q is invalid; expected unix:///path/to/socket", b.URL)
		}
	default:
		return fmt.Errorf("buildKitURL=%q is invalid; expected a tcp:// or unix:// address", b.URL)
	}

	return nil
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
