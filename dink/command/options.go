package command

import (
	"encoding/json"
	"fmt"

	"github.com/sysson/dink/dink/pkg/config"
	"github.com/urfave/cli/v3"
)

const (
	defaultConfigFile string = "/etc/dink/config.json"
	defaultEnvPrefix  string = "DINK"
)

type options struct {
	cfg        *config.Config
	flags      *[]cli.Flag
	configFile string
}

func newOptions(cfg *config.Config) *options {
	return &options{
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
			Sources:     cli.EnvVars(defaultEnvPrefix + "_LOGLEVEL"),
			Destination: &o.cfg.LogLevel,
		},
		&cli.StringFlag{
			Name:        "accessLogLevel",
			Usage:       "Access log level: debug|info|warn|error",
			Sources:     cli.EnvVars(defaultEnvPrefix + "_ACCESSLOGLEVEL"),
			Destination: &o.cfg.AccessLogLevel,
		},
		&cli.StringFlag{
			Name:        "serverVersion",
			Usage:       "Server version reported by API",
			Sources:     cli.EnvVars(defaultEnvPrefix + "_SERVERVERSION"),
			Destination: &o.cfg.ServerVersion,
		},
		&cli.StringFlag{
			Name:        "minAPIVersion",
			Usage:       "Minimum Docker API version supported",
			Sources:     cli.EnvVars(defaultEnvPrefix + "_MINAPIVERSION"),
			Destination: &o.cfg.MinAPIVersion,
		},
		&cli.StringFlag{
			Name:        "apiVersion",
			Usage:       "Maximum Docker API version supported",
			Sources:     cli.EnvVars(defaultEnvPrefix + "_APIVERSION"),
			Destination: &o.cfg.APIVersion,
		},
		&cli.StringFlag{
			Name:        "kubeConfigPath",
			Usage:       "Path to kubeconfig; empty means in-cluster/default",
			Sources:     cli.EnvVars(defaultEnvPrefix + "_KUBECONFIGPATH"),
			Destination: &o.cfg.KubeConfigPath,
		},
		&cli.StringFlag{
			Name:        "namespace",
			Usage:       "Base namespace used by dink",
			Sources:     cli.EnvVars(defaultEnvPrefix + "_NAMESPACE"),
			Destination: &o.cfg.Namespace,
		},
		&cli.StringFlag{
			Name:        "port",
			Usage:       "Plaintext HTTP listen port",
			Sources:     cli.EnvVars(defaultEnvPrefix + "_PORT"),
			Destination: &o.cfg.Port,
		},
		&cli.StringFlag{
			Name:        "tlsPort",
			Usage:       "TLS HTTPS listen port",
			Sources:     cli.EnvVars(defaultEnvPrefix+"_TLSPORT", defaultEnvPrefix+"_TLS_PORT"),
			Destination: &o.cfg.TLSPort,
		},
		&cli.StringFlag{
			Name:        "buildKitAddress",
			Usage:       "BuildKit endpoint address",
			Sources:     cli.EnvVars(defaultEnvPrefix + "_BUILDKITADDRESS"),
			Destination: &o.cfg.BuildKitAddress,
		},
		&cli.StringFlag{
			Name:        "pluginDir",
			Usage:       "Plugin directory path",
			Sources:     cli.EnvVars(defaultEnvPrefix + "_PLUGINDIR"),
			Destination: &o.cfg.PluginDir,
		},
		&cli.BoolFlag{
			Name:        "disableTLS",
			Usage:       "Disable TLS and serve plaintext only",
			Sources:     cli.EnvVars(defaultEnvPrefix + "_DISABLETLS"),
			Destination: o.cfg.DisableTLS,
		},
		&cli.BoolFlag{
			Name:        "allowPlaintextWithTLS",
			Value:       false,
			Usage:       "Allow plaintext HTTP listener even when TLS is enabled",
			Sources:     cli.EnvVars(defaultEnvPrefix + "_ALLOWPLAINTEXTWITHTLS"),
			Destination: o.cfg.AllowPlaintextWithTLS,
		},
		&cli.StringFlag{
			Name:        "tlsCertFile",
			Usage:       "Path to TLS server certificate PEM",
			Sources:     cli.EnvVars(defaultEnvPrefix+"_TLSCERTFILE", defaultEnvPrefix+"_TLS_CERT_FILE"),
			Destination: &o.cfg.TLSCertFile,
		},
		&cli.StringFlag{
			Name:        "tlsKeyFile",
			Usage:       "Path to TLS server private key PEM",
			Sources:     cli.EnvVars(defaultEnvPrefix+"_TLSKEYFILE", defaultEnvPrefix+"_TLS_KEY_FILE"),
			Destination: &o.cfg.TLSKeyFile,
		},
		&cli.StringFlag{
			Name:        "clientCAFile",
			Sources:     cli.EnvVars(defaultEnvPrefix + "_CLIENTCAFILE"),
			Usage:       "Path to client CA bundle PEM for mTLS",
			Destination: &o.cfg.ClientCAFile,
		},
		&cli.StringFlag{
			Name:        "minTLSVersion",
			Usage:       "Minimum TLS version: 1.2|1.3",
			Sources:     cli.EnvVars(defaultEnvPrefix + "_MINTLSVERSION"),
			Destination: &o.cfg.MinTLSVersion,
		},
		&cli.BoolFlag{
			Name:        "disableNamespaceCreation",
			Usage:       "Disable automatic namespace creation",
			Sources:     cli.EnvVars(defaultEnvPrefix + "_DISABLENAMESPACECREATION"),
			Destination: o.cfg.DisableNamespaceCreation,
		},
	}...)
}

func mergeConfig(cfg *config.Config, opts *options) error {
	values := map[string]any{}
	if opts.flags != nil {
		for _, flag := range *opts.flags {
			if flag.IsSet() && flag.Names()[0] != "config" {
				values[flag.Names()[0]] = flag.Get()
			}
		}
	}
	data, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("encoding flag configuration: %w", err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("merging flag configuration: %w", err)
	}

	return nil
}
