package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"time"

	"github.com/sysson/dink/dink/pkg/certs"
	"github.com/urfave/cli/v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	defaultCertDir    = ".certs"
	defaultSecretName = "dink-tls"
	defaultService    = "dink"
	certFieldManager  = "dink-certs"
)

type certsOptions struct {
	outDir      string
	namespace   string
	secretName  string
	serviceName string
	domain      string
	keyType     string
	rsaBits     int
	caDuration  time.Duration
	duration    time.Duration
	extraDNS    []string
	extraIPs    []string
	kubeConfig  string
	force       bool
	apply       bool
}

func certsCmd(stdout, stderr io.Writer) *cli.Command {
	o := new(certsOptions)

	return &cli.Command{
		Name:  "certs",
		Usage: "Generate the CA, server and client certificates used for mTLS",
		Description: "Issues a self-signed CA, a dink server certificate and a docker client\n" +
			"certificate, writes them to disk and applies the server keypair to the\n" +
			"cluster as a Secret. An existing CA in the output directory is reused so\n" +
			"that clients already trusting it keep working; pass --force to rotate it.\n\n" +
			"The CA private key is never uploaded: the cluster only holds the server\n" +
			"keypair and the public ca.crt used to verify docker clients.",
		Writer:    stdout,
		ErrWriter: stderr,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "out",
				Usage:       "Directory to write the certificates to",
				Value:       defaultCertDir,
				Sources:     cli.EnvVars(defaultEnvPrefix + "_CERTS_OUT"),
				Destination: &o.outDir,
			},
			&cli.StringFlag{
				Name:        "namespace",
				Usage:       "Namespace holding the TLS Secret",
				Value:       "dink-system",
				Sources:     cli.EnvVars(defaultEnvPrefix + "_K8S_NAMESPACE"),
				Destination: &o.namespace,
			},
			&cli.StringFlag{
				Name:        "secretName",
				Usage:       "Name of the TLS Secret",
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
				Value:       string(certs.DefaultKeyType),
				Destination: &o.keyType,
			},
			&cli.IntFlag{
				Name:        "rsaBits",
				Usage:       "RSA key size, only used with --keyType=rsa",
				Value:       certs.DefaultRSABits,
				Destination: &o.rsaBits,
			},
			&cli.DurationFlag{
				Name:        "caValidity",
				Usage:       "Validity period of the CA certificate",
				Value:       certs.DefaultCADuration,
				Destination: &o.caDuration,
			},
			&cli.DurationFlag{
				Name:        "validity",
				Usage:       "Validity period of the leaf certificates",
				Value:       certs.DefaultDuration,
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
			&cli.StringFlag{
				Name:        "kubeconfig",
				Usage:       "Path to a kubeconfig; defaults to the ambient cluster configuration",
				Sources:     cli.EnvVars("KUBECONFIG"),
				Destination: &o.kubeConfig,
			},
			&cli.BoolFlag{
				Name:        "force",
				Usage:       "Rotate the CA and reissue everything, invalidating existing clients",
				Destination: &o.force,
			},
			&cli.BoolFlag{
				Name:        "apply",
				Usage:       "Apply the generated server keypair to the cluster as a Secret",
				Value:       true,
				Destination: &o.apply,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return generateCerts(ctx, o, cmd.Writer)
		},
	}
}

func generateCerts(ctx context.Context, o *certsOptions, out io.Writer) error {
	opts, err := o.certOptions()
	if err != nil {
		return err
	}

	ca, err := o.authority(opts, out)
	if err != nil {
		return err
	}

	bundle, err := ca.Bundle()
	if err != nil {
		return fmt.Errorf("issuing certificates: %w", err)
	}

	if err := certs.Save(o.outDir, bundle); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "wrote certificates to %s\n", o.outDir)

	if !o.apply {
		return nil
	}

	if err := o.applySecret(ctx, bundle); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "applied secret %s/%s\n", o.namespace, o.secretName)

	printDockerEnv(out, o)
	return nil
}

func (o *certsOptions) certOptions() (certs.Options, error) {
	ips := make([]net.IP, 0, len(o.extraIPs))
	for _, raw := range o.extraIPs {
		ip := net.ParseIP(raw)
		if ip == nil {
			return certs.Options{}, fmt.Errorf("invalid IP address %q", raw)
		}
		ips = append(ips, ip)
	}

	return certs.Options{
		KeyType:       certs.KeyType(o.keyType),
		RSABits:       o.rsaBits,
		CADuration:    o.caDuration,
		Duration:      o.duration,
		ServiceName:   o.serviceName,
		Namespace:     o.namespace,
		ClusterDomain: o.domain,
		ExtraDNSNames: o.extraDNS,
		ExtraIPs:      ips,
	}, nil
}

func (o *certsOptions) authority(opts certs.Options, out io.Writer) (*certs.Authority, error) {
	if !o.force {
		ca, err := certs.LoadAuthorityDir(o.outDir, opts)
		switch {
		case err == nil:
			_, _ = fmt.Fprintf(out, "reusing existing CA in %s\n", o.outDir)
			return ca, nil
		case !errors.Is(err, certs.ErrNoAuthority):
			return nil, err
		}
	}

	_, _ = fmt.Fprintf(out, "generating a new %s CA\n", opts.KeyType)
	return certs.NewAuthority(opts)
}

func (o *certsOptions) applySecret(ctx context.Context, bundle *certs.Bundle) error {
	client, err := o.kubeClient()
	if err != nil {
		return err
	}

	ns := &corev1.Namespace{Name: o.namespace}
	if _, err := client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensuring namespace %s: %w", o.namespace, err)
	}

	secret := &corev1.Secret{
		Name:      o.secretName,
		Namespace: o.namespace,
		Labels: map[string]string{
			"app.kubernetes.io/name":      "dink",
			"app.kubernetes.io/component": "tls",
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"tls.crt": bundle.Server.Cert,
			"tls.key": bundle.Server.Key,
			"ca.crt":  bundle.CA.Cert,
		},
	}

	secrets := client.CoreV1().Secrets(o.namespace)
	_, err = secrets.Update(ctx, secret, metav1.UpdateOptions{FieldManager: certFieldManager})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, secret, metav1.CreateOptions{FieldManager: certFieldManager})
	}
	if err != nil {
		return fmt.Errorf("applying secret %s/%s: %w", o.namespace, o.secretName, err)
	}
	return nil
}

func (o *certsOptions) kubeClient() (*kubernetes.Clientset, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if o.kubeConfig != "" {
		rules.ExplicitPath = o.kubeConfig
	}
	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig (re-run with --apply=false to only write files): %w", err)
	}
	return kubernetes.NewForConfig(restConfig)
}

func printDockerEnv(out io.Writer, o *certsOptions) {
	dockerDir, err := filepath.Abs(filepath.Join(o.outDir, certs.DockerDir))
	if err != nil {
		dockerDir = filepath.Join(o.outDir, certs.DockerDir)
	}
	_, _ = fmt.Fprintf(out, `
to talk to dink from this machine:

  kubectl -n %s port-forward svc/%s 2376:2376 &
  export DOCKER_HOST=tcp://localhost:2376
  export DOCKER_TLS_VERIFY=1
  export DOCKER_CERT_PATH=%s
  docker info
`, o.namespace, o.serviceName, dockerDir)
}
