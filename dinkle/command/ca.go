package command

import (
	"context"
	"errors"

	"github.com/sysson/dink/pkg/types"
	"github.com/sysson/syskit/pki"
	"github.com/urfave/cli/v3"
)

// caOptions holds the flags shared by `ca generate` and `ca rotate`. opts
// holds the pki.Options values whose flags bind directly to it; keyType still
// needs converting before it fits pki.Options.
type caOptions struct {
	opts pki.Options

	defaultNamespace string
	systemNamespace  string
	caSecretName     string
	keyType          string
	apply            bool
}

func caCmd(o *Options) *cli.Command {
	return &cli.Command{
		Name:  "ca",
		Usage: "Generate, rotate and inspect dink's certificate authority",
		Commands: []*cli.Command{
			caGenerateCmd(o),
			caRotateCmd(o),
			caInfoCmd(o),
			caSyncCmd(o),
		},
	}
}

func caFlags(c *caOptions) []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:        "systemNamespace",
			Usage:       "dink's system namespace",
			Value:       defaultSystemNamespace,
			Sources:     cli.EnvVars(types.DefaultEnvPrefix + "_K8S_SYSTEM_NAMESPACE"),
			Destination: &c.systemNamespace,
		},
		&cli.StringFlag{
			Name:        "defaultNamespace",
			Usage:       "dink's default (anonymous) namespace",
			Value:       defaultNamespace,
			Sources:     cli.EnvVars(types.DefaultEnvPrefix + "_K8S_DEFAULT_NAMESPACE"),
			Destination: &c.defaultNamespace,
		},
		&cli.StringFlag{
			Name:        "caSecretName",
			Usage:       "Name of the CA Secret; restrict read access to trusted operators",
			Value:       defaultCASecretName,
			Destination: &c.caSecretName,
		},
		&cli.StringFlag{
			Name:        "keyType",
			Usage:       "Key algorithm: ecdsa|ed25519|rsa",
			Value:       string(pki.DefaultKeyType),
			Destination: &c.keyType,
		},
		&cli.IntFlag{
			Name:        "rsaBits",
			Usage:       "RSA key size, only used with --keyType=rsa",
			Value:       pki.DefaultRSABits,
			Destination: &c.opts.RSABits,
		},
		&cli.DurationFlag{
			Name:        "validity",
			Usage:       "Validity period of the certificate",
			Value:       pki.DefaultDuration,
			Destination: &c.opts.Duration,
		},
		&cli.BoolFlag{
			Name:        "apply",
			Usage:       "Ensure the namespaces exist and apply the CA keypair to the cluster as a Secret",
			Value:       true,
			Destination: &c.apply,
		},
	}
}

func caGenerateCmd(o *Options) *cli.Command {
	c := &caOptions{}

	return &cli.Command{
		Name:  "generate",
		Usage: "Create the CA and dink's namespaces if they don't already exist",
		Description: "Reuses the CA cached in --certsDir if present, otherwise pulls it from the\n" +
			"cluster's CA Secret so that running this on a second host converges on\n" +
			"the same CA instead of minting a new one. Only if neither is found is a\n" +
			"new CA generated.\n\n" +
			"The CA keypair is applied to the cluster as a Secret so every host\n" +
			"managing dink can issue server, tenant and client certificates\n" +
			"consistent with each other.",
		Flags: caFlags(c),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return newService(o, c).GenerateCA(ctx, cmd.Writer)
		},
	}
}

func caRotateCmd(o *Options) *cli.Command {
	c := &caOptions{}
	var confirm bool

	return &cli.Command{
		Name:  "rotate",
		Usage: "Generate a new CA, invalidating every existing server and client certificate",
		Description: "Every client certificate issued by the previous CA stops being trusted:\n" +
			"server certificates must be reissued with `dinkle server issue`, and\n" +
			"tenants and clients must be recreated with `dinkle tenant create` /\n" +
			"`dinkle client create` after rotating.",
		Flags: append(caFlags(c), &cli.BoolFlag{
			Name:        "yes",
			Usage:       "Confirm the rotation",
			Destination: &confirm,
		}),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if !confirm {
				return errors.New("rotating the CA invalidates every existing client certificate; pass --yes to confirm")
			}
			return newService(o, c).RotateCA(ctx, cmd.Writer)
		},
	}
}

func caInfoCmd(o *Options) *cli.Command {
	c := &caOptions{}
	return &cli.Command{
		Name:  "info",
		Usage: "Show the CA cached locally and the CA stored in the cluster, and whether they match",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "systemNamespace",
				Value:       defaultSystemNamespace,
				Sources:     cli.EnvVars(types.DefaultEnvPrefix + "_K8S_SYSTEM_NAMESPACE"),
				Destination: &c.systemNamespace,
			},
			&cli.StringFlag{
				Name:        "caSecretName",
				Value:       defaultCASecretName,
				Destination: &c.caSecretName,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return newService(o, c).CAInfo(ctx, cmd.Writer)
		},
	}
}

func caSyncCmd(o *Options) *cli.Command {
	var systemNamespace, caSecretName string
	var push bool

	return &cli.Command{
		Name:  "sync",
		Usage: "Reconcile the local CA cache with the cluster's CA Secret",
		Description: "By default pulls the cluster's CA into the local cache, since the cluster\n" +
			"is the shared source of truth across hosts. Pass --push to instead\n" +
			"upload the local CA to the cluster.",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "systemNamespace",
				Value:       defaultSystemNamespace,
				Sources:     cli.EnvVars(types.DefaultEnvPrefix + "_K8S_SYSTEM_NAMESPACE"),
				Destination: &systemNamespace,
			},
			&cli.StringFlag{
				Name:        "caSecretName",
				Value:       defaultCASecretName,
				Destination: &caSecretName,
			},
			&cli.BoolFlag{
				Name:        "push",
				Usage:       "Upload the local CA to the cluster instead of pulling it",
				Destination: &push,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return newService(o, &caOptions{}).SyncCA(ctx, systemNamespace, caSecretName, push, cmd.Writer)
		},
	}
}

func (c *caOptions) validate() error {
	c.opts.KeyType = pki.KeyType(c.keyType)
	c.opts.CommonName = defaultCACommonName
	c.opts.Organization = defaultOrganization
	return nil
}
