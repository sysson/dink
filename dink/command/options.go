package command

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"strconv"

	"github.com/sysson/dink/dink/pkg/config"
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
			DefaultText: o.defaults.LogLevel,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_LOGLEVEL"),
			Destination: &o.cfg.LogLevel,
		},
		&cli.StringFlag{
			Name:        "accessLogLevel",
			Usage:       "Access log level: debug|info|warn|error",
			DefaultText: o.defaults.AccessLogLevel,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_ACCESSLOGLEVEL"),
			Destination: &o.cfg.AccessLogLevel,
		},
		&cli.StringFlag{
			Name:        "serverVersion",
			Usage:       "Server version reported by API",
			DefaultText: o.defaults.ServerVersion,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_SERVERVERSION"),
			Destination: &o.cfg.ServerVersion,
		},
		&cli.StringFlag{
			Name:        "minAPIVersion",
			Usage:       "Minimum Docker API version supported",
			DefaultText: o.defaults.MinAPIVersion,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_MINAPIVERSION"),
			Destination: &o.cfg.MinAPIVersion,
		},
		&cli.StringFlag{
			Name:        "apiVersion",
			Usage:       "Maximum Docker API version supported",
			DefaultText: o.defaults.APIVersion,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_APIVERSION"),
			Destination: &o.cfg.APIVersion,
		},
		&cli.StringFlag{
			Name:        "kubeConfigPath",
			Usage:       "Path to kubeconfig; empty means in-cluster/default",
			DefaultText: o.defaults.KubeConfigPath,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_KUBECONFIGPATH"),
			Destination: &o.cfg.KubeConfigPath,
		},
		&cli.StringFlag{
			Name:        "namespace",
			Usage:       "Base namespace used by dink",
			DefaultText: o.defaults.Namespace,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_NAMESPACE"),
			Destination: &o.cfg.Namespace,
		},
		&cli.StringFlag{
			Name:        "host",
			Usage:       "Address to bind listeners to; empty means all interfaces",
			DefaultText: o.defaults.Host,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_HOST"),
			Destination: &o.cfg.Host,
		},
		&cli.StringFlag{
			Name:        "port",
			Usage:       "Plaintext HTTP listen port",
			DefaultText: o.defaults.Port,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_PORT"),
			Destination: &o.cfg.Port,
		},
		&cli.StringFlag{
			Name:        "tlsPort",
			Usage:       "TLS HTTPS listen port",
			DefaultText: o.defaults.TLSPort,
			Sources:     cli.EnvVars(defaultEnvPrefix+"_TLSPORT", defaultEnvPrefix+"_TLS_PORT"),
			Destination: &o.cfg.TLSPort,
		},
		&cli.StringFlag{
			Name:        "buildKitAddress",
			Usage:       "BuildKit endpoint address",
			DefaultText: o.defaults.BuildKitAddress,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_BUILDKITADDRESS"),
			Destination: &o.cfg.BuildKitAddress,
		},
		&cli.StringFlag{
			Name:        "pluginDir",
			Usage:       "Plugin directory path",
			DefaultText: o.defaults.PluginDir,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_PLUGINDIR"),
			Destination: &o.cfg.PluginDir,
		},
		&cli.BoolFlag{
			Name:        "disableTLS",
			Usage:       "Disable TLS and serve plaintext only",
			DefaultText: strconv.FormatBool(isBool(o.defaults.DisableTLS)),
			Sources:     cli.EnvVars(defaultEnvPrefix + "_DISABLETLS"),
			Action:      setPtr(&o.cfg.DisableTLS),
		},
		&cli.BoolFlag{
			Name:        "allowPlaintextWithTLS",
			Usage:       "Allow plaintext HTTP listener even when TLS is enabled",
			DefaultText: strconv.FormatBool(isBool(o.defaults.AllowPlaintextWithTLS)),
			Sources:     cli.EnvVars(defaultEnvPrefix + "_ALLOWPLAINTEXTWITHTLS"),
			Action:      setPtr(&o.cfg.AllowPlaintextWithTLS),
		},
		&cli.StringFlag{
			Name:        "tlsCertFile",
			Usage:       "Path to TLS server certificate PEM",
			DefaultText: o.defaults.TLSCertFile,
			Sources:     cli.EnvVars(defaultEnvPrefix+"_TLSCERTFILE", defaultEnvPrefix+"_TLS_CERT_FILE"),
			Destination: &o.cfg.TLSCertFile,
		},
		&cli.StringFlag{
			Name:        "tlsKeyFile",
			Usage:       "Path to TLS server private key PEM",
			DefaultText: o.defaults.TLSKeyFile,
			Sources:     cli.EnvVars(defaultEnvPrefix+"_TLSKEYFILE", defaultEnvPrefix+"_TLS_KEY_FILE"),
			Destination: &o.cfg.TLSKeyFile,
		},
		&cli.StringFlag{
			Name:        "clientCAFile",
			Usage:       "Path to client CA bundle PEM for mTLS",
			DefaultText: o.defaults.ClientCAFile,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_CLIENTCAFILE"),
			Destination: &o.cfg.ClientCAFile,
		},
		&cli.StringFlag{
			Name:        "minTLSVersion",
			Usage:       "Minimum TLS version: 1.2|1.3",
			DefaultText: o.defaults.MinTLSVersion,
			Sources:     cli.EnvVars(defaultEnvPrefix + "_MINTLSVERSION"),
			Destination: &o.cfg.MinTLSVersion,
		},
		&cli.BoolFlag{
			Name:        "disableNamespaceCreation",
			Usage:       "Disable automatic namespace creation",
			DefaultText: strconv.FormatBool(isBool(o.defaults.DisableNamespaceCreation)),
			Sources:     cli.EnvVars(defaultEnvPrefix + "_DISABLENAMESPACECREATION"),
			Action:      setPtr(&o.cfg.DisableNamespaceCreation),
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
