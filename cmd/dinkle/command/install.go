package command

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/sysson/dink/cmd/dinkle/namespaces"
	"github.com/sysson/dink/cmd/dinkle/store"
	"github.com/sysson/dink/core/version"
	"github.com/sysson/syskit/pki"
	"github.com/urfave/cli/v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var releaseTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$`)

type installationOptions struct {
	release  string
	yes      bool
	timeout  time.Duration
	dnsNames []string
}

func installationCmd(o *Options, upgrade bool) *cli.Command {
	name, usage := "install", "Bootstrap certificates and install a matched Dink release"
	if upgrade {
		name, usage = "upgrade", "Apply a matched Dink release without changing certificates"
	}
	opts := &installationOptions{}
	return &cli.Command{
		Name:  name,
		Usage: usage,
		Description: "Downloads checksum-verified release manifests and applies them using kubectl.\n" +
			"Uses dink-system and dink-default. Includes cluster RBAC, the host-mounted\n" +
			"dinki-node DaemonSet and the privileged BuildKit backend; review the release\n" +
			"manifests before using --yes. Does not configure external network access.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "version", Usage: "Release tag; defaults to this binary's release (required for development builds)", Destination: &opts.release},
			&cli.BoolFlag{Name: "yes", Usage: "Confirm applying the release's cluster-wide and privileged resources", Destination: &opts.yes},
			&cli.DurationFlag{Name: "timeout", Value: 5 * time.Minute, Usage: "Timeout for the complete installation or upgrade", Destination: &opts.timeout},
			&cli.StringSliceFlag{Name: "dnsName", Usage: "Additional Dink API certificate DNS SAN on first installation; repeatable", Destination: &opts.dnsNames},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if !opts.yes {
				return errors.New("review the release manifests and pass --yes to confirm installation of privileged cluster resources")
			}
			tag, err := installationVersion(opts.release, version.Get().Version)
			if err != nil {
				return err
			}
			if opts.timeout <= 0 {
				return errors.New("timeout must be positive")
			}
			if upgrade && len(opts.dnsNames) != 0 {
				return errors.New("upgrade preserves certificates; use `dinkle server issue` to change DNS SANs")
			}
			if _, err := exec.LookPath("kubectl"); err != nil {
				return fmt.Errorf("kubectl is required: %w", err)
			}
			ctx, cancel := context.WithTimeout(ctx, opts.timeout)
			defer cancel()
			manifest, err := fetchInstallation(ctx, &http.Client{Timeout: time.Minute},
				"https://github.com/sysson/dink/releases/download/"+tag)
			if err != nil {
				return err
			}
			if err := prepareInstallation(ctx, o, upgrade, opts.dnsNames, cmd.Writer); err != nil {
				return err
			}
			run := func(input io.Reader, args ...string) error {
				if o.kubeConfig != "" {
					args = append([]string{"--kubeconfig", o.kubeConfig}, args...)
				}
				process := exec.CommandContext(ctx, "kubectl", args...)
				process.Stdin, process.Stdout, process.Stderr = input, cmd.Writer, cmd.ErrWriter
				if err := process.Run(); err != nil {
					if ctx.Err() != nil {
						return fmt.Errorf("kubectl %s: %w", strings.Join(args, " "), ctx.Err())
					}
					return fmt.Errorf("kubectl %s: %w", strings.Join(args, " "), err)
				}
				return nil
			}
			return applyInstallation(run, manifest, tag, cmd.Writer)
		},
	}
}

func installationVersion(requested, built string) (string, error) {
	if requested == "" {
		requested = built
	}
	if !releaseTag.MatchString(requested) {
		return "", fmt.Errorf("invalid release version %q; provide an explicit vX.Y.Z release tag with --version for development builds", requested)
	}
	return requested, nil
}

func fetchInstallation(ctx context.Context, client *http.Client, baseURL string) ([]byte, error) {
	fetch := func(name string, limit int64) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/"+name, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("downloading %s: %w", name, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("downloading %s: %s (this release must include installation assets)", name, resp.Status)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", name, err)
		}
		if int64(len(data)) > limit {
			return nil, fmt.Errorf("%s exceeds size limit", name)
		}
		return data, nil
	}
	checksum, err := fetch("install.yaml.sha256", 1024)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(checksum))
	if len(fields) != 2 || fields[1] != "install.yaml" {
		return nil, errors.New("invalid installation checksum file")
	}
	expected, err := hex.DecodeString(fields[0])
	if err != nil || len(expected) != sha256.Size {
		return nil, errors.New("invalid installation SHA256 digest")
	}
	manifest, err := fetch("install.yaml", 8<<20)
	if err != nil {
		return nil, err
	}
	actual := sha256.Sum256(manifest)
	if len(manifest) == 0 || !bytes.Equal(expected, actual[:]) {
		return nil, errors.New("installation manifest checksum mismatch")
	}
	return manifest, nil
}

func applyInstallation(run func(io.Reader, ...string) error, manifest []byte, tag string, out io.Writer) error {
	if err := run(bytes.NewReader(manifest), "apply", "-f", "-"); err != nil {
		return err
	}
	for _, resource := range []string{"deployment/dinki", "daemonset/dinki-node", "deployment/buildkit", "deployment/dink"} {
		if err := run(nil, "-n", defaultSystemNamespace, "rollout", "status", resource, "--timeout=0"); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(out, "Dink %s is ready in %s. Configure private API access separately, then create tenants with `dinkle tenant create`.\n", tag, defaultSystemNamespace)
	return err
}

func prepareInstallation(ctx context.Context, o *Options, upgrade bool, dnsNames []string, out io.Writer) error {
	kc, err := o.kubeClient(ctx)
	if err != nil {
		return err
	}
	caStore := namespaces.NewSecretStore(kc, defaultSystemNamespace, defaultCASecretName)
	clusterCA, err := caStore.LoadCA(ctx)
	if err != nil && !errors.Is(err, pki.ErrNoAuthority) {
		return fmt.Errorf("reading installation CA: %w", err)
	}
	hasCA := err == nil
	if upgrade && !hasCA {
		return errors.New("installation CA is missing; use `dinkle install` for a new installation")
	}
	if !hasCA {
		_, err := kc.AppsV1().Deployments(defaultSystemNamespace).Get(ctx, "dink", metav1.GetOptions{})
		if err == nil {
			return errors.New("dink exists but its CA is missing; restore the CA before installing")
		}
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("checking existing installation: %w", err)
		}
	}
	if hasCA {
		if err := validateInstallationCA(clusterCA); err != nil {
			return fmt.Errorf("invalid installation CA: %w", err)
		}
	}
	secrets := make(map[string]*corev1.Secret)
	for _, name := range []string{defaultServerSecretName, "dinki-tls", "buildkit-tls"} {
		secret, err := kc.CoreV1().Secrets(defaultSystemNamespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if upgrade {
				return fmt.Errorf("required certificate Secret %s/%s is missing; repair it with `dinkle server issue`", defaultSystemNamespace, name)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("reading certificate %s: %w", name, err)
		}
		if !hasCA {
			return fmt.Errorf("certificate %s exists but the installation CA is missing; restore the CA before installing", name)
		}
		if err := validateInstallationCertificate(secret, clusterCA); err != nil {
			return fmt.Errorf("certificate %s is invalid; repair it with `dinkle server issue`: %w", name, err)
		}
		if name == defaultServerSecretName && !upgrade {
			pair, err := tls.X509KeyPair(secret.Data["tls.crt"], secret.Data["tls.key"])
			if err != nil {
				return err
			}
			cert, err := x509.ParseCertificate(pair.Certificate[0])
			if err != nil {
				return err
			}
			for _, dnsName := range dnsNames {
				if err := cert.VerifyHostname(dnsName); err != nil {
					return fmt.Errorf("existing Dink certificate does not cover %q; use `dinkle server issue` to change DNS SANs: %w", dnsName, err)
				}
			}
		}
		secrets[name] = secret
	}
	if upgrade {
		return nil
	}
	localStore := store.NewFileStore(o.certsDir)
	localCA, err := localStore.LoadCA(ctx)
	if err != nil && !errors.Is(err, pki.ErrNoAuthority) {
		return err
	}
	if err == nil {
		if err := validateInstallationCA(localCA); err != nil {
			return fmt.Errorf("invalid local installation CA: %w", err)
		}
	}
	if hasCA {
		if err == nil && !bytes.Equal(localCA.Cert, clusterCA.Cert) {
			return errors.New("local and cluster CAs differ; use a separate --certsDir or reconcile with `dinkle ca sync`")
		}
		if err := localStore.SaveCA(ctx, clusterCA); err != nil {
			return err
		}
	}
	s := newService(o, &caOptions{
		opts:    pki.Options{RSABits: pki.DefaultRSABits, Duration: pki.DefaultDuration},
		keyType: string(pki.DefaultKeyType), apply: true,
		systemNamespace: defaultSystemNamespace, defaultNamespace: "dink-default", caSecretName: defaultCASecretName,
	})
	if err := s.GenerateCA(ctx, out); err != nil {
		return err
	}
	for _, target := range []struct{ service, secret string }{
		{"dink", defaultServerSecretName}, {"dinki", "dinki-tls"}, {"buildkit", "buildkit-tls"},
	} {
		if secrets[target.secret] != nil {
			_, _ = fmt.Fprintf(out, "preserving certificate %s/%s\n", defaultSystemNamespace, target.secret)
			continue
		}
		opts := &serverOptions{
			systemNamespace: defaultSystemNamespace, caSecretName: defaultCASecretName,
			serviceName: target.service, serverSecretName: target.secret, clusterDomain: "cluster.local", apply: true,
		}
		if target.service == "dink" {
			opts.extraDNSNames = dnsNames
		}
		if err := s.IssueServer(ctx, opts, out); err != nil {
			return err
		}
	}
	return nil
}

func validateInstallationCertificate(secret *corev1.Secret, ca pki.KeyPair) error {
	pair, err := tls.X509KeyPair(secret.Data["tls.crt"], secret.Data["tls.key"])
	if err != nil {
		return err
	}

	if !bytes.Equal(secret.Data["ca.crt"], ca.Cert) {
		return errors.New("certificate CA does not match installation CA")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca.Cert) {
		return errors.New("invalid CA certificate")
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	_, err = cert.Verify(x509.VerifyOptions{
		Roots: roots, DNSName: strings.TrimSuffix(secret.Name, "-tls") + "." + defaultSystemNamespace + ".svc",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}},
	)
	return err
}
