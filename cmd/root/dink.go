package root

import (
	"context"
	"fmt"

	"github.com/sysson/dink/cmd/version"
	"github.com/sysson/dink/pkg/config"
	"github.com/sysson/dink/pkg/dink"
	"github.com/urfave/cli/v3"
)

func Cmd() *cli.Command {
	return &cli.Command{
		Name:    "dink",
		Usage:   "A Kubernetes API compatibility proxy",
		Version: version.GetVersion(),
		Action:  dinkAction,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "config",
				Value: "",
				Usage: "Path to the configuration file",
			},
			&cli.StringFlag{
				Name:  "logLevel",
				Usage: "Log level: debug|info|warn|error",
			},
			&cli.StringFlag{
				Name:  "accessLogLevel",
				Usage: "Access log level: debug|info|warn|error",
			},
			&cli.StringFlag{
				Name:  "serverVersion",
				Usage: "Server version reported by API",
			},
			&cli.StringFlag{
				Name:  "minAPIVersion",
				Usage: "Minimum Docker API version supported",
			},
			&cli.StringFlag{
				Name:  "apiVersion",
				Usage: "Maximum Docker API version supported",
			},
			&cli.StringFlag{
				Name:  "kubeConfigPath",
				Usage: "Path to kubeconfig; empty means in-cluster/default",
			},
			&cli.StringFlag{
				Name:  "namespace",
				Usage: "Base namespace used by dink",
			},
			&cli.StringFlag{
				Name:  "port",
				Usage: "Plaintext HTTP listen port",
			},
			&cli.StringFlag{
				Name:  "tlsPort",
				Usage: "TLS HTTPS listen port",
			},
			&cli.StringFlag{
				Name:  "envPrefix",
				Usage: "Environment variable prefix (applies to future loads)",
			},
			&cli.StringFlag{
				Name:  "buildKitAddress",
				Usage: "BuildKit endpoint address",
			},
			&cli.StringFlag{
				Name:  "authPlugins",
				Usage: "Auth plugins JSON array, e.g. [{\"name\":\"n\",\"path\":\"/plugin\"}]",
			},
			&cli.StringFlag{
				Name:  "pluginDir",
				Usage: "Plugin directory path",
			},
			&cli.BoolFlag{
				Name:  "disableTLS",
				Usage: "Disable TLS and serve plaintext only",
			},
			&cli.BoolFlag{
				Name:  "allowPlaintextWithTLS",
				Value: false,
				Usage: "Allow plaintext HTTP listener even when TLS is enabled",
			},
			&cli.StringFlag{
				Name:  "tlsCertFile",
				Usage: "Path to TLS server certificate PEM",
			},
			&cli.StringFlag{
				Name:  "tlsKeyFile",
				Usage: "Path to TLS server private key PEM",
			},
			&cli.StringFlag{
				Name:  "clientCAFile",
				Usage: "Path to client CA bundle PEM for mTLS",
			},
			&cli.StringFlag{
				Name:  "minTLSVersion",
				Usage: "Minimum TLS version: 1.2|1.3",
			},
			&cli.BoolFlag{
				Name:  "disableNamespaceCreation",
				Usage: "Disable automatic namespace creation",
			},
		},
	}
}

func dinkAction(ctx context.Context, cmd *cli.Command) error {

	cfg := config.NewConfig()
	err := cfg.SetEffectiveConfig(cmd)
	if err != nil {
		return fmt.Errorf("unable to set config :%w", err)
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	svr, err := dink.Init(ctx, cfg)
	if err != nil {
		return err
	}
	return svr.Run(ctx)
}
