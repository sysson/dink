package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	LogLevel                 string       `json:"logLevel,omitempty"`
	AccessLogLevel           string       `json:"accessLogLevel,omitempty"`
	ServerVersion            string       `json:"serverVersion,omitempty"`
	MinAPIVersion            string       `json:"minAPIVersion,omitempty"`
	APIVersion               string       `json:"apiVersion,omitempty"`
	KubeConfigPath           string       `json:"kubeConfigPath,omitempty"`
	Namespace                string       `json:"namespace,omitempty"`
	Port                     string       `json:"port,omitempty"`
	TLSPort                  string       `json:"tlsPort,omitempty"`
	BuildKitAddress          string       `json:"buildKitAddress,omitempty"`
	AuthPlugins              []AuthPlugin `json:"authPlugins,omitempty"`
	PluginDir                string       `json:"pluginDir,omitempty"`
	DisableTLS               *bool        `json:"disableTLS,omitempty"`
	AllowPlaintextWithTLS    *bool        `json:"allowPlaintextWithTLS,omitempty"`
	TLSCertFile              string       `json:"tlsCertFile,omitempty"`
	TLSKeyFile               string       `json:"tlsKeyFile,omitempty"`
	ClientCAFile             string       `json:"clientCAFile,omitempty"`
	MinTLSVersion            string       `json:"minTLSVersion,omitempty"`
	DisableNamespaceCreation *bool        `json:"disableNamespaceCreation,omitempty"`
}

type AuthPlugin struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func New() *Config {
	return &Config{
		LogLevel:                 "info",
		AccessLogLevel:           "error",
		ServerVersion:            "v1.0.0",
		MinAPIVersion:            "1.40",
		APIVersion:               "1.55",
		KubeConfigPath:           "",
		Namespace:                "dink",
		Port:                     "2375",
		TLSPort:                  "2376",
		DisableTLS:               new(false),
		AllowPlaintextWithTLS:    new(false),
		MinTLSVersion:            "1.2",
		DisableNamespaceCreation: new(false),
		AuthPlugins:              []AuthPlugin{},
		PluginDir:                "/var/lib/dink/plugins",
	}
}

func Load(configFile string) (*Config, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", configFile, err)
	}

	cfg := new(Config)
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("decoding config file %q: %w", configFile, err)
	}

	return cfg, nil
}

func (c *Config) Validate() error {
	errs := []error{}

	if c.Namespace == "" {
		errs = append(errs, errors.New("namespace must not be empty"))
	}

	if err := validateLogLevel("logLevel", c.LogLevel); err != nil {
		errs = append(errs, err)
	}
	if err := validateLogLevel("accessLogLevel", c.AccessLogLevel); err != nil {
		errs = append(errs, err)
	}

	if err := validatePort("port", c.Port); err != nil {
		errs = append(errs, err)
	}

	if c.DisableTLS != nil && *c.DisableTLS {
		if c.ClientCAFile != "" {
			errs = append(errs, errors.New("clientCAFile is set but disableTLS=true; client CA is only used when TLS is enabled"))
		}
	} else {
		if c.TLSPort == "" {
			errs = append(errs, errors.New("tlsPort must not be empty when TLS is enabled"))
		} else if err := validatePort("tlsPort", c.TLSPort); err != nil {
			errs = append(errs, err)
		}

		if c.TLSCertFile == "" {
			errs = append(errs, errors.New("tlsCertFile must not be empty when TLS is enabled"))
		} else if err := validateExistingFile("tlsCertFile", c.TLSCertFile); err != nil {
			errs = append(errs, err)
		}

		if c.TLSKeyFile == "" {
			errs = append(errs, errors.New("tlsKeyFile must not be empty when TLS is enabled"))
		} else if err := validateExistingFile("tlsKeyFile", c.TLSKeyFile); err != nil {
			errs = append(errs, err)
		}

		if c.ClientCAFile != "" {
			if err := validateExistingFile("clientCAFile", c.ClientCAFile); err != nil {
				errs = append(errs, err)
			}
		}

		if err := validateMinTLSVersion(c.MinTLSVersion); err != nil {
			errs = append(errs, err)
		}

		if c.AllowPlaintextWithTLS != nil && *c.AllowPlaintextWithTLS && c.Port == c.TLSPort {
			errs = append(errs, errors.New("port and tlsPort must be different when allowPlaintextWithTLS=true"))
		}
	}

	for i, plugin := range c.AuthPlugins {
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

func validatePort(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	p, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("%s=%q is invalid; expected a numeric port", field, value)
	}
	if p < 1 || p > 65535 {
		return fmt.Errorf("%s=%q is invalid; expected range 1-65535", field, value)
	}
	return nil
}

func validateExistingFile(field, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s=%q is invalid: %w", field, path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s=%q is invalid: expected a file, got directory", field, path)
	}
	return nil
}
