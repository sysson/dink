package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/cliflagv3"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/urfave/cli/v3"
)

type Config struct {
	LogLevel                 string       `json:"logLevel"`
	AccessLogLevel           string       `json:"accessLogLevel"`
	ServerVersion            string       `json:"serverVersion"`
	MinAPIVersion            string       `json:"minAPIVersion"`
	APIVersion               string       `json:"apiVersion"`
	KubeConfigPath           string       `json:"kubeConfigPath"`
	Namespace                string       `json:"namespace"`
	Port                     string       `json:"port"`
	TLSPort                  string       `json:"tlsPort"`
	EnvPrefix                string       `json:"envPrefix"`
	BuildKitAddress          string       `json:"buildKitAddress"`
	AuthPlugins              []AuthPlugin `json:"authPlugins"`
	PluginDir                string       `json:"pluginDir"`
	DisableTLS               bool         `json:"disableTLS"`
	AllowPlaintextWithTLS    bool         `json:"allowPlaintextWithTLS"`
	TLSCertFile              string       `json:"tlsCertFile"`
	TLSKeyFile               string       `json:"tlsKeyFile"`
	ClientCAFile             string       `json:"clientCAFile"`
	MinTLSVersion            string       `json:"minTLSVersion"`
	DisableNamespaceCreation bool         `json:"disableNamespaceCreation"`
}

type AuthPlugin struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func NewConfig() *Config {
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
		DisableTLS:               false,
		AllowPlaintextWithTLS:    false,
		MinTLSVersion:            "1.2",
		DisableNamespaceCreation: false,
		EnvPrefix:                "DINK",
		AuthPlugins:              []AuthPlugin{},
		PluginDir:                "/var/lib/dink/plugins",
	}
}

func (c *Config) SetEffectiveConfig(cmd *cli.Command) error {
	k := koanf.New(".")
	var config = ""
	if cmd.String("config") != "" {
		config = cmd.String("config")
	}

	if config != "" {
		if err := k.Load(file.Provider(config), yaml.Parser()); err != nil {
			println("No configuration file found at " + config + ", using defaults and environment variables")
		}
	}

	if err := k.Load(env.Provider(".", env.Opt{
		Prefix: c.EnvPrefix,
		TransformFunc: func(k, v string) (string, any) {
			k = strings.ReplaceAll(strings.ToLower((strings.TrimPrefix(k, c.EnvPrefix+"_"))), "__", ".")
			if strings.Contains(v, " ") {
				return k, strings.Split(v, " ")
			}
			return k, v
		},
	}), nil); err != nil {
		return err
	}

	if err := k.Load(cliflagv3.Provider(cmd, "."), nil); err != nil {
		return err
	}

	if err := k.Unmarshal("", c); err != nil {
		return err
	}

	if cmd.Name != "" && k.Exists(cmd.Name) {
		if err := k.Unmarshal(cmd.Name, c); err != nil {
			return err
		}
	}

	return nil
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

	if c.DisableTLS {
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

		if c.AllowPlaintextWithTLS && c.Port == c.TLSPort {
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
