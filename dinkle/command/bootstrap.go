package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/sysson/dink/pkg/certs"
	"github.com/sysson/dink/pkg/types"
	"github.com/urfave/cli/v3"
)

const (
	defaultSecretName = "dink-tls"
	defaultService    = "dink"
)

type bootstrapOptions struct {
	o *Options

	systemNamespace  string
	defaultNamespace string
	secretName       string
	serviceName      string
	domain           string
	keyType          string
	rsaBits          int
	caDuration       time.Duration
	duration         time.Duration
	extraDNS         []string
	extraIPs         []string
	force            bool
	apply            bool
}

func bootstrapCmd(o *Options) *cli.Command {
	b := &bootstrapOptions{o: o}

	return &cli.Command{
		Name:  "bootstrap",
		Usage: "Create the CA, dink's namespaces and the server certificate if they don't already exist",
		Description: "Issues a self-signed CA if one is not already cached in --certsDir, ensures\n" +
			"dink's system and default namespaces exist, and issues the server\n" +
			"certificate dink uses for mTLS, applying it to the cluster as a Secret.\n\n" +
			"The CA private key is never uploaded: the cluster only holds the server\n" +
			"keypair and the public ca.crt used to verify docker clients.",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "systemNamespace",
				Usage:       "dink's system namespace",
				Value:       types.DefaultSystemNamespace,
				Sources:     cli.EnvVars(types.DefaultEnvPrefix + "_K8S_SYSTEM_NAMESPACE"),
				Destination: &b.systemNamespace,
			},
			&cli.StringFlag{
				Name:        "defaultNamespace",
				Usage:       "dink's default (anonymous) namespace",
				Value:       types.DefaultNamespace,
				Sources:     cli.EnvVars(types.DefaultEnvPrefix + "_K8S_DEFAULT_NAMESPACE"),
				Destination: &b.defaultNamespace,
			},
			&cli.StringFlag{
				Name:        "secretName",
				Usage:       "Name of the server TLS Secret",
				Value:       defaultSecretName,
				Destination: &b.secretName,
			},
			&cli.StringFlag{
				Name:        "serviceName",
				Usage:       "Name of the dink Service, used for the server certificate SANs",
				Value:       defaultService,
				Destination: &b.serviceName,
			},
			&cli.StringFlag{
				Name:        "clusterDomain",
				Usage:       "Cluster DNS domain",
				Value:       "cluster.local",
				Destination: &b.domain,
			},
			&cli.StringFlag{
				Name:        "keyType",
				Usage:       "Key algorithm: ecdsa|ed25519|rsa",
				Value:       string(certs.DefaultKeyType),
				Destination: &b.keyType,
			},
			&cli.IntFlag{
				Name:        "rsaBits",
				Usage:       "RSA key size, only used with --keyType=rsa",
				Value:       certs.DefaultRSABits,
				Destination: &b.rsaBits,
			},
			&cli.DurationFlag{
				Name:        "caValidity",
				Usage:       "Validity period of the CA certificate",
				Value:       certs.DefaultCADuration,
				Destination: &b.caDuration,
			},
			&cli.DurationFlag{
				Name:        "validity",
				Usage:       "Validity period of the server certificate",
				Value:       certs.DefaultDuration,
				Destination: &b.duration,
			},
			&cli.StringSliceFlag{
				Name:        "dnsName",
				Usage:       "Additional DNS SAN for the server certificate; repeatable",
				Destination: &b.extraDNS,
			},
			&cli.StringSliceFlag{
				Name:        "ip",
				Usage:       "Additional IP SAN for the server certificate; repeatable",
				Destination: &b.extraIPs,
			},
			&cli.BoolFlag{
				Name:        "force",
				Usage:       "Rotate the CA and reissue the server certificate, invalidating existing clients",
				Destination: &b.force,
			},
			&cli.BoolFlag{
				Name:        "apply",
				Usage:       "Ensure the namespaces exist and apply the server keypair to the cluster as a Secret",
				Value:       true,
				Destination: &b.apply,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return newService(o).Bootstrap(ctx, b, cmd.Writer)
		},
	}
}

func (b *bootstrapOptions) certOptions() (certs.Options, error) {
	ips := make([]net.IP, 0, len(b.extraIPs))
	for _, raw := range b.extraIPs {
		ip := net.ParseIP(raw)
		if ip == nil {
			return certs.Options{}, fmt.Errorf("invalid IP address %q", raw)
		}
		ips = append(ips, ip)
	}

	return certs.Options{
		KeyType:       certs.KeyType(b.keyType),
		RSABits:       b.rsaBits,
		CADuration:    b.caDuration,
		Duration:      b.duration,
		ServiceName:   b.serviceName,
		Namespace:     b.systemNamespace,
		ClusterDomain: b.domain,
		ExtraDNSNames: b.extraDNS,
		ExtraIPs:      ips,
	}, nil
}

func (b *bootstrapOptions) authority(opts certs.Options, out io.Writer) (*certs.Authority, error) {
	if !b.force {
		ca, err := certs.LoadAuthorityDir(b.o.certsDir, opts)
		switch {
		case err == nil:
			_, _ = fmt.Fprintf(out, "reusing existing CA in %s\n", b.o.certsDir)
			return ca, nil
		case !errors.Is(err, certs.ErrNoAuthority):
			return nil, err
		}
	}

	_, _ = fmt.Fprintf(out, "generating a new %s CA\n", opts.KeyType)
	return certs.NewAuthority(opts)
}
