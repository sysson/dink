package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/sysson/dink/dinkle/namespaces"
	pkgcerts "github.com/sysson/dink/pkg/certs"
	"github.com/sysson/dink/pkg/types"
	"github.com/urfave/cli/v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	defaultSecretName = "dink-tls"
	defaultService    = "dink"
	certFieldManager  = "dinkle"
)

type bootstrapOptions struct {
	g *globalOptions

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

func bootstrapCmd(g *globalOptions) *cli.Command {
	o := &bootstrapOptions{g: g}

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
				Destination: &o.systemNamespace,
			},
			&cli.StringFlag{
				Name:        "defaultNamespace",
				Usage:       "dink's default (anonymous) namespace",
				Value:       types.DefaultNamespace,
				Sources:     cli.EnvVars(types.DefaultEnvPrefix + "_K8S_DEFAULT_NAMESPACE"),
				Destination: &o.defaultNamespace,
			},
			&cli.StringFlag{
				Name:        "secretName",
				Usage:       "Name of the server TLS Secret",
				Value:       defaultSecretName,
				Destination: &o.secretName,
			},
			&cli.StringFlag{
				Name:        "serviceName",
				Usage:       "Name of the dink Service, used for the server certificate SANs",
				Value:       defaultService,
				Destination: &o.serviceName,
			},
			&cli.StringFlag{
				Name:        "clusterDomain",
				Usage:       "Cluster DNS domain",
				Value:       "cluster.local",
				Destination: &o.domain,
			},
			&cli.StringFlag{
				Name:        "keyType",
				Usage:       "Key algorithm: ecdsa|ed25519|rsa",
				Value:       string(pkgcerts.DefaultKeyType),
				Destination: &o.keyType,
			},
			&cli.IntFlag{
				Name:        "rsaBits",
				Usage:       "RSA key size, only used with --keyType=rsa",
				Value:       pkgcerts.DefaultRSABits,
				Destination: &o.rsaBits,
			},
			&cli.DurationFlag{
				Name:        "caValidity",
				Usage:       "Validity period of the CA certificate",
				Value:       pkgcerts.DefaultCADuration,
				Destination: &o.caDuration,
			},
			&cli.DurationFlag{
				Name:        "validity",
				Usage:       "Validity period of the server certificate",
				Value:       pkgcerts.DefaultDuration,
				Destination: &o.duration,
			},
			&cli.StringSliceFlag{
				Name:        "dnsName",
				Usage:       "Additional DNS SAN for the server certificate; repeatable",
				Destination: &o.extraDNS,
			},
			&cli.StringSliceFlag{
				Name:        "ip",
				Usage:       "Additional IP SAN for the server certificate; repeatable",
				Destination: &o.extraIPs,
			},
			&cli.BoolFlag{
				Name:        "force",
				Usage:       "Rotate the CA and reissue the server certificate, invalidating existing clients",
				Destination: &o.force,
			},
			&cli.BoolFlag{
				Name:        "apply",
				Usage:       "Ensure the namespaces exist and apply the server keypair to the cluster as a Secret",
				Value:       true,
				Destination: &o.apply,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return bootstrap(ctx, o, cmd.Writer)
		},
	}
}

func bootstrap(ctx context.Context, o *bootstrapOptions, out io.Writer) error {
	opts, err := o.certOptions()
	if err != nil {
		return err
	}

	ca, err := o.authority(opts, out)
	if err != nil {
		return err
	}
	if err := pkgcerts.SaveCA(o.g.certsDir, ca.KeyPair()); err != nil {
		return err
	}

	server, err := ca.IssueServer()
	if err != nil {
		return fmt.Errorf("issuing server certificate: %w", err)
	}
	if err := pkgcerts.SaveServer(o.g.certsDir, server); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "wrote certificates to %s\n", o.g.certsDir)

	if !o.apply {
		return nil
	}

	client, err := o.g.kubeClient()
	if err != nil {
		return err
	}

	nsManager := namespaces.New(client)
	if err := nsManager.Ensure(ctx, o.systemNamespace, false); err != nil {
		return err
	}
	if err := nsManager.Ensure(ctx, o.defaultNamespace, false); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "ensured namespaces %s, %s\n", o.systemNamespace, o.defaultNamespace)

	if err := applyServerSecret(ctx, client, o, ca.KeyPair(), server); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "applied secret %s/%s\n", o.systemNamespace, o.secretName)
	return nil
}

func (o *bootstrapOptions) certOptions() (pkgcerts.Options, error) {
	ips := make([]net.IP, 0, len(o.extraIPs))
	for _, raw := range o.extraIPs {
		ip := net.ParseIP(raw)
		if ip == nil {
			return pkgcerts.Options{}, fmt.Errorf("invalid IP address %q", raw)
		}
		ips = append(ips, ip)
	}

	return pkgcerts.Options{
		KeyType:       pkgcerts.KeyType(o.keyType),
		RSABits:       o.rsaBits,
		CADuration:    o.caDuration,
		Duration:      o.duration,
		ServiceName:   o.serviceName,
		Namespace:     o.systemNamespace,
		ClusterDomain: o.domain,
		ExtraDNSNames: o.extraDNS,
		ExtraIPs:      ips,
	}, nil
}

func (o *bootstrapOptions) authority(opts pkgcerts.Options, out io.Writer) (*pkgcerts.Authority, error) {
	if !o.force {
		ca, err := pkgcerts.LoadAuthorityDir(o.g.certsDir, opts)
		switch {
		case err == nil:
			_, _ = fmt.Fprintf(out, "reusing existing CA in %s\n", o.g.certsDir)
			return ca, nil
		case !errors.Is(err, pkgcerts.ErrNoAuthority):
			return nil, err
		}
	}

	_, _ = fmt.Fprintf(out, "generating a new %s CA\n", opts.KeyType)
	return pkgcerts.NewAuthority(opts)
}

func applyServerSecret(ctx context.Context, client kubernetes.Interface, o *bootstrapOptions, ca, server pkgcerts.KeyPair) error {
	secret := &corev1.Secret{
			Name:      o.secretName,
			Namespace: o.systemNamespace,
			Labels: map[string]string{
				namespaces.LabelManagedBy: namespaces.ManagedByValue,
				"app.kubernetes.io/name":  "dink",
			},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"tls.crt": server.Cert,
			"tls.key": server.Key,
			"ca.crt":  ca.Cert,
		},
	}

	secrets := client.CoreV1().Secrets(o.systemNamespace)
	_, err := secrets.Update(ctx, secret, metav1.UpdateOptions{FieldManager: certFieldManager})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, secret, metav1.CreateOptions{FieldManager: certFieldManager})
	}
	if err != nil {
		return fmt.Errorf("applying secret %s/%s: %w", o.systemNamespace, o.secretName, err)
	}
	return nil
}
