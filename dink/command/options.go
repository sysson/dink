package command

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"strconv"

	"github.com/sysson/dink/dink/config"
	"github.com/urfave/cli/v3"
)

const (
	defaultConfigFile string = "/etc/dink/config.json"
	defaultEnvPrefix  string = "DINK"
)

type options struct {
	defaults   *config.Config
	cfg        *config.Config
	flags      *[]cli.Flag
	configFile string
}

func newOptions(defaults, cfg *config.Config) *options {
	return &options{
		defaults:   defaults,
		cfg:        cfg,
		configFile: defaultConfigFile,
	}
}

func (o *options) addFlags(flags *[]cli.Flag) {
	*flags = append(*flags, []cli.Flag{
		&cli.StringFlag{
			Name:        "config",
			Usage:       "Path to the configuration file",
			Value:       defaultConfigFile,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_CONFIG"),
			Destination: &o.configFile,
		},
		&cli.StringFlag{
			Name:        "logLevel",
			Usage:       "Log level: debug|info|warn|error",
			DefaultText: o.defaults.Log.Level,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_LOG_LEVEL"),
			Destination: &o.cfg.Log.Level,
		},
		&cli.StringFlag{
			Name:        "accessLogLevel",
			Usage:       "Access log level: debug|info|warn|error",
			DefaultText: o.defaults.AccessLog.Level,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_ACCESS_LOG_LEVEL"),
			Destination: &o.cfg.AccessLog.Level,
		},
		&cli.StringFlag{
			Name:        "namespace",
			Usage:       "Base namespace used by dink",
			DefaultText: o.defaults.Kubernetes.Namespace,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_K8S_NAMESPACE"),
			Destination: &o.cfg.Kubernetes.Namespace,
		},
		&cli.StringFlag{
			Name:        "host",
			Usage:       "Address to bind listeners to; empty means all interfaces",
			DefaultText: o.defaults.Server.Host,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_SERVER_HOST"),
			Destination: &o.cfg.Server.Host,
		},
		&cli.StringFlag{
			Name:        "port",
			Usage:       "Plaintext HTTP listen port",
			DefaultText: o.defaults.Server.Port,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_SERVER_PORT"),
			Destination: &o.cfg.Server.Port,
		},
		&cli.StringFlag{
			Name:        "tlsPort",
			Usage:       "TLS HTTPS listen port",
			DefaultText: o.defaults.Server.TLSPort,
			Sources:     cli.EnvVars(defaultEnvPrefix+"_SERVER_TLSPORT", defaultEnvPrefix+"_SERVER_TLS_PORT"),
			Destination: &o.cfg.Server.TLSPort,
		},
		&cli.StringFlag{
			Name:        "healthPort",
			Usage:       "Plain HTTP health listen port",
			DefaultText: o.defaults.Kubernetes.HealthPort,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_K8S_HEALTHPORT"),
			Destination: &o.cfg.Kubernetes.HealthPort,
		},
		&cli.StringFlag{
			Name:        "buildKitURL",
			Usage:       "BuildKit endpoint address",
			DefaultText: o.defaults.BuildKit.URL,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_BUILDKIT_URL"),
			Destination: &o.cfg.BuildKit.URL,
		},
		&cli.StringFlag{
			Name:        "pluginDir",
			Usage:       "Plugin directory path",
			DefaultText: o.defaults.Auth.PluginDir,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_AUTH_PLUGINDIR"),
			Destination: &o.cfg.Auth.PluginDir,
		},
		&cli.BoolFlag{
			Name:        "disableTLS",
			Usage:       "Disable TLS and serve plaintext only",
			DefaultText: strconv.FormatBool(isBool(o.defaults.Server.DisableTLS)),
			Sources:     cli.EnvVars(defaultEnvPrefix+"_SERVER_DISABLETLS", defaultEnvPrefix+"_SERVER_DISABLE_TLS"),
			Action:      setPtr(&o.cfg.Server.DisableTLS),
		},
		&cli.BoolFlag{
			Name:        "allowPlaintextWithTLS",
			Usage:       "Allow plaintext HTTP listener even when TLS is enabled",
			DefaultText: strconv.FormatBool(isBool(o.defaults.Server.AllowPlaintextWithTLS)),
			Sources:     cli.EnvVars(defaultEnvPrefix+"_SERVER_ALLOWPLAINTEXTWITHTLS", defaultEnvPrefix+"_SERVER_ALLOW_PLAINTEXT_WITH_TLS"),
			Action:      setPtr(&o.cfg.Server.AllowPlaintextWithTLS),
		},
		&cli.StringFlag{
			Name:        "tlsCertFile",
			Usage:       "Path to TLS server certificate PEM",
			DefaultText: o.defaults.TLS.CertFile,
			Sources:     cli.EnvVars(defaultEnvPrefix+"_TLS_CERTFILE", defaultEnvPrefix+"_TLS_CERT_FILE"),
			Destination: &o.cfg.TLS.CertFile,
		},
		&cli.StringFlag{
			Name:        "tlsKeyFile",
			Usage:       "Path to TLS server private key PEM",
			DefaultText: o.defaults.TLS.KeyFile,
			Sources:     cli.EnvVars(defaultEnvPrefix+"_TLS_KEYFILE", defaultEnvPrefix+"_TLS_KEY_FILE"),
			Destination: &o.cfg.TLS.KeyFile,
		},
		&cli.StringFlag{
			Name:        "clientCAFile",
			Usage:       "Path to client CA bundle PEM for mTLS",
			DefaultText: o.defaults.TLS.ClientCAFile,
			Sources:     cli.EnvVars(defaultEnvPrefix+"_TLS_CLIENTCAFILE", defaultEnvPrefix+"_TLS_CLIENT_CA_FILE"),
			Destination: &o.cfg.TLS.ClientCAFile,
		},
		&cli.StringFlag{
			Name:        "minTLSVersion",
			Usage:       "Minimum TLS version: 1.2|1.3",
			DefaultText: o.defaults.TLS.MinTLSVersion,
			Sources:     cli.EnvVars(defaultEnvPrefix+"_TLS_MINTLSVERSION", defaultEnvPrefix+"_TLS_MIN_TLS_VERSION"),
			Destination: &o.cfg.TLS.MinTLSVersion,
		},
		&cli.BoolFlag{
			Name:        "disableNamespaceCreation",
			Usage:       "Disable automatic namespace creation",
			DefaultText: strconv.FormatBool(isBool(o.defaults.Kubernetes.NamespaceCreationEnabled)),
			Sources:     cli.EnvVars(defaultEnvPrefix+"_K8S_DISABLENAMESPACECREATION", defaultEnvPrefix+"_K8S_DISABLE_NAMESPACE_CREATION"),
			Action:      setPtr(&o.cfg.Kubernetes.NamespaceCreationEnabled),
		},
	}...)
}

// setPtr runs only when a flag is set, leaving unset fields nil so they do not
// override config file or default values during the merge.
func setPtr[T any](dst **T) func(context.Context, *cli.Command, T) error {
	return func(_ context.Context, _ *cli.Command, value T) error {
		*dst = new(value)
		return nil
	}
}

func loadConfigFile(configFile string) (*config.Config, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", configFile, err)
	}

	cfg := new(config.Config)
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("decoding config file %q: %w", configFile, err)
	}

	return cfg, nil
}

func mergeConfig(src, dst *config.Config) error {
	data, err := json.Marshal(src)
	if err != nil {
		return fmt.Errorf("encoding source configuration: %w", err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("merging source configuration: %w", err)
	}

	return nil
}
