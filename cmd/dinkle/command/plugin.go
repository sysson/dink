package command

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sysson/syskit/pki"
	"github.com/urfave/cli/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sysson/dink/cmd/dinkle/namespaces"
	"github.com/sysson/dink/cmd/dinkle/store"
	"github.com/sysson/dink/core/plugins"
)

var ErrPluginNameRequired = errors.New("plugin name is required")

const defaultClusterDomain = "cluster.local"

type pluginOptions struct {
	namespace     string
	service       string
	port          int
	types         []string
	timeout       string
	insecure      bool
	secretName    string
	clusterDomain string
}

func pluginCmd(o *Options) *cli.Command {
	return &cli.Command{
		Name:  "plugin",
		Usage: "Register dink plugins and issue their certificates",
		Description: "Plugins registered in the system namespace apply to every tenant; plugins\n" +
			"registered in a tenant namespace apply to that tenant only.",
		Commands: []*cli.Command{
			pluginIssueCmd(o),
			pluginAddCmd(o),
			pluginListCmd(o),
			pluginRemoveCmd(o),
		},
	}
}

func pluginNamespaceFlag(p *pluginOptions) cli.Flag {
	return &cli.StringFlag{
		Name:        "namespace",
		Aliases:     []string{"n"},
		Usage:       "Namespace the plugin runs in: the system namespace for all tenants, or a tenant",
		Value:       defaultSystemNamespace,
		Destination: &p.namespace,
	}
}

func pluginIssueCmd(o *Options) *cli.Command {
	p := &pluginOptions{}
	return &cli.Command{
		Name:      "issue",
		Usage:     "Issue a plugin's server certificate into a TLS Secret in its namespace",
		ArgsUsage: "<service>",
		Flags: []cli.Flag{
			pluginNamespaceFlag(p),
			&cli.StringFlag{
				Name:        "secretName",
				Usage:       "Secret to write the certificate to (default <service>-tls)",
				Destination: &p.secretName,
			},
			&cli.StringFlag{
				Name:        "clusterDomain",
				Value:       defaultClusterDomain,
				Destination: &p.clusterDomain,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			p.service = cmd.Args().First()
			if err := requireString(p.service, errors.New("service name is required")); err != nil {
				return err
			}
			return newService(o, &caOptions{}).IssuePluginCert(ctx, p, cmd.Writer)
		},
	}
}

func pluginAddCmd(o *Options) *cli.Command {
	p := &pluginOptions{}
	return &cli.Command{
		Name:      "add",
		Usage:     "Register a plugin, or update its registration",
		ArgsUsage: "<name>",
		Flags: []cli.Flag{
			pluginNamespaceFlag(p),
			&cli.StringSliceFlag{
				Name:        "type",
				Usage:       "Plugin type it implements: auth, secrets or volumes (repeatable)",
				Required:    true,
				Destination: &p.types,
			},
			&cli.StringFlag{
				Name:        "service",
				Usage:       "Service in the plugin's namespace that serves it (default <name>)",
				Destination: &p.service,
			},
			&cli.IntFlag{
				Name:        "port",
				Value:       8443,
				Destination: &p.port,
			},
			&cli.StringFlag{
				Name:        "timeout",
				Usage:       "Per-call timeout, e.g. 2s",
				Destination: &p.timeout,
			},
			&cli.BoolFlag{
				Name:        "insecure",
				Usage:       "Call the plugin over plaintext HTTP; for development only",
				Destination: &p.insecure,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			name := cmd.Args().First()
			if err := requireString(name, ErrPluginNameRequired); err != nil {
				return err
			}
			return newService(o, &caOptions{}).AddPlugin(ctx, name, p, cmd.Writer)
		},
	}
}

func pluginListCmd(o *Options) *cli.Command {
	p := &pluginOptions{}
	return &cli.Command{
		Name:  "list",
		Usage: "List registered plugins and their health",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "namespace",
				Aliases:     []string{"n"},
				Usage:       "Only list plugins in this namespace",
				Destination: &p.namespace,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return newService(o, &caOptions{}).ListPlugins(ctx, p.namespace, cmd.Writer)
		},
	}
}

func pluginRemoveCmd(o *Options) *cli.Command {
	p := &pluginOptions{}
	return &cli.Command{
		Name:      "remove",
		Usage:     "Remove a plugin registration",
		ArgsUsage: "<name>",
		Flags:     []cli.Flag{pluginNamespaceFlag(p)},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			name := cmd.Args().First()
			if err := requireString(name, ErrPluginNameRequired); err != nil {
				return err
			}
			return newService(o, &caOptions{}).RemovePlugin(ctx, p.namespace, name, cmd.Writer)
		},
	}
}

// IssuePluginCert issues a server-only certificate, so it can never authenticate to dink as a client.
func (s *Service) IssuePluginCert(ctx context.Context, p *pluginOptions, out io.Writer) error {
	caPair, err := s.loadExistingCA(ctx, store.NewFileStore(s.options.certsDir), defaultSystemNamespace, defaultCASecretName, out)
	if err != nil {
		return err
	}
	ca, err := pki.LoadAuthority(caPair)
	if err != nil {
		return err
	}
	host := fmt.Sprintf("%s.%s.svc", p.service, p.namespace)
	leaf, err := ca.Issue(
		pki.WithCommonName(host),
		pki.WithExtKeyUsage([]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}),
		pki.WithDNSNames([]string{
			p.service,
			p.service + "." + p.namespace,
			host,
			host + "." + p.clusterDomain,
		}),
	)
	if err != nil {
		return fmt.Errorf("issuing plugin certificate: %w", err)
	}

	kc, err := s.options.kubeClient(ctx)
	if err != nil {
		return err
	}
	if _, err := kc.CoreV1().Namespaces().Get(ctx, p.namespace, metav1.GetOptions{}); err != nil {
		return fmt.Errorf("namespace %q: %w", p.namespace, err)
	}
	secretName := p.secretName
	if secretName == "" {
		secretName = p.service + "-tls"
	}
	if err := namespaces.NewSecretStore(kc, p.namespace, "").SaveLeaf(ctx, secretName, caPair, leaf); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "wrote plugin certificate for %s to secret %s/%s\n", host, p.namespace, secretName)
	return nil
}

func (s *Service) AddPlugin(ctx context.Context, name string, p *pluginOptions, out io.Writer) error {
	spec := plugins.Spec{
		Service:  plugins.ServiceRef{Name: p.service, Port: p.port},
		Timeout:  p.timeout,
		Insecure: p.insecure,
	}
	if spec.Service.Name == "" {
		spec.Service.Name = name
	}
	for _, t := range p.types {
		spec.Types = append(spec.Types, plugins.Type(t))
	}
	if p.timeout != "" {
		if d, err := time.ParseDuration(p.timeout); err != nil || d <= 0 {
			return fmt.Errorf("invalid timeout %q", p.timeout)
		}
	}
	obj, err := spec.Object(p.namespace, name)
	if err != nil {
		return err
	}

	dc, err := s.options.dynamicClient(ctx)
	if err != nil {
		return err
	}
	client := dc.Resource(plugins.Resource).Namespace(p.namespace)
	existing, err := client.Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if _, err := client.Create(ctx, obj, metav1.CreateOptions{}); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "registered plugin %s/%s\n", p.namespace, name)
	case err != nil:
		return err
	default:
		existing.Object["spec"] = obj.Object["spec"]
		if _, err := client.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "updated plugin %s/%s\n", p.namespace, name)
	}
	if p.insecure {
		_, _ = fmt.Fprintln(out, "warning: the plugin is called over plaintext HTTP")
	}
	return nil
}

func (s *Service) ListPlugins(ctx context.Context, namespace string, out io.Writer) error {
	dc, err := s.options.dynamicClient(ctx)
	if err != nil {
		return err
	}
	list, err := dc.Resource(plugins.Resource).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAMESPACE\tNAME\tTYPES\tENDPOINT\tREADY\tVERSION\tMESSAGE")
	for i := range list.Items {
		item := &list.Items[i]
		status := plugins.StatusFromObject(item)
		types, endpoint := "?", "?"
		if spec, err := plugins.SpecFromObject(item); err == nil {
			parts := make([]string, 0, len(spec.Types))
			for _, t := range spec.Types {
				parts = append(parts, string(t))
			}
			types, endpoint = strings.Join(parts, ","), spec.Endpoint(item.GetNamespace())
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%t\t%s\t%s\n",
			item.GetNamespace(), item.GetName(), types, endpoint, status.Ready, status.Version, status.Message)
	}
	return w.Flush()
}

func (s *Service) RemovePlugin(ctx context.Context, namespace, name string, out io.Writer) error {
	dc, err := s.options.dynamicClient(ctx)
	if err != nil {
		return err
	}
	if err := dc.Resource(plugins.Resource).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "removed plugin %s/%s\n", namespace, name)
	return nil
}
