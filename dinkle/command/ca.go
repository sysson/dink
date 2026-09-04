package command

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/sysson/dink/pkg/types"
	"github.com/sysson/syskit/pki"
	"github.com/urfave/cli/v3"
)

// caOptions holds the flags shared by `ca generate` and `ca rotate`. opts
// holds every field also present on pki.Options (RSABits, CADuration,
// ClusterDomain, RSABits, CADuration, Duration, ExtraDNSNames) so their
// flags bind directly to it instead of duplicating the fields here; keyType
// and extraIPs still need converting before they fit certs.Options.
type caOptions struct {
	opts pki.Options

	defaultNamespace string
	systemNamespace  string
	caSecretName     string
	serverSecretName string
	serviceName      string
	keyType          string
	clusterDomain    string
	extraDNSNames    []string
	extraIPs         []string
	validExtraIPs    []net.IP
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
			Name:        "serverSecretName",
			Usage:       "Name of the server TLS Secret",
			Value:       defaultServerSecretName,
			Destination: &c.serverSecretName,
		},
		&cli.StringFlag{
			Name:        "serviceName",
			Usage:       "Name of the dink Service, used for the server certificate SANs",
			Value:       defaultServiceName,
			Destination: &c.serviceName,
		},
		&cli.StringFlag{
			Name:        "clusterDomain",
			Usage:       "Cluster DNS domain",
			Value:       "cluster.local",
			Destination: &c.clusterDomain,
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
			Name:        "caValidity",
			Usage:       "Validity period of the CA certificate",
			Value:       pki.DefaultCADuration,
			Destination: &c.opts.CADuration,
		},
		&cli.DurationFlag{
			Name:        "validity",
			Usage:       "Validity period of the server certificate",
			Value:       pki.DefaultDuration,
			Destination: &c.opts.Duration,
		},
		&cli.StringSliceFlag{
			Name:        "dnsName",
			Usage:       "Additional DNS SAN for the server certificate; repeatable",
			Destination: &c.extraDNSNames,
		},
		&cli.StringSliceFlag{
			Name:        "ip",
			Usage:       "Additional IP SAN for the server certificate; repeatable",
			Destination: &c.extraIPs,
		},
		&cli.BoolFlag{
			Name:        "apply",
			Usage:       "Ensure the namespaces exist and apply the CA and server keypair to the cluster as Secrets",
			Value:       true,
			Destination: &c.apply,
		},
	}
}

func caGenerateCmd(o *Options) *cli.Command {
	c := &caOptions{}

	return &cli.Command{
		Name:  "generate",
		Usage: "Create the CA, dink's namespaces and the server certificate if they don't already exist",
		Description: "Reuses the CA cached in --certsDir if present, otherwise pulls it from the\n" +
			"cluster's CA Secret so that running this on a second host converges on\n" +
			"the same CA instead of minting a new one. Only if neither is found is a\n" +
			"new CA generated.\n\n" +
			"The CA keypair and the server certificate are applied to the cluster as\n" +
			"Secrets so every host managing dink can issue tenant and client\n" +
			"certificates consistent with each other.",
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
		Usage: "Generate a new CA and reissue the server certificate, invalidating every existing client certificate",
		Description: "Every client certificate issued by the previous CA stops being trusted:\n" +
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
	ips := make([]net.IP, 0, len(c.extraIPs))
	for _, raw := range c.extraIPs {
		ip := net.ParseIP(raw)
		if ip == nil {
			return fmt.Errorf("invalid IP address %q", raw)
		}
		ips = append(ips, ip)
	}
	c.validExtraIPs = ips
	c.opts.KeyType = pki.KeyType(c.keyType)
	c.opts.CACommonName = defaultCACommonName
	c.opts.Organization = defaultOrganization
	return nil
}
