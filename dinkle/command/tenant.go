package command

import (
	"context"
	"fmt"
	"io"
	"os"

	dinklecerts "github.com/sysson/dink/dinkle/certs"
	"github.com/sysson/dink/dinkle/namespaces"
	"github.com/urfave/cli/v3"
)

// defaultTenantClient is the client certificate every tenant is issued at
// creation time, before any additional `dinkle client create` calls.
const defaultTenantClient = "default"

func tenantCmd(g *globalOptions) *cli.Command {
	return &cli.Command{
		Name:  "tenant",
		Usage: "Create, list and delete tenants",
		Commands: []*cli.Command{
			tenantCreateCmd(g),
			tenantListCmd(g),
			tenantDeleteCmd(g),
		},
	}
}

func tenantCreateCmd(g *globalOptions) *cli.Command {
	return &cli.Command{
		Name:      "create",
		Usage:     "Create a tenant namespace and issue its default client certificate",
		ArgsUsage: "<name>",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			name := cmd.Args().First()
			if name == "" {
				return fmt.Errorf("tenant name is required")
			}
			return tenantCreate(ctx, g, name, cmd.Writer)
		},
	}
}

func tenantCreate(ctx context.Context, g *globalOptions, name string, out io.Writer) error {
	ca, err := loadAuthority(g)
	if err != nil {
		return err
	}

	kc, err := g.kubeClient()
	if err != nil {
		return err
	}

	if err := namespaces.New(kc).Ensure(ctx, name, true); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "ensured tenant namespace %s\n", name)

	return issueClientCert(ctx, g, kc, ca, name, defaultTenantClient, out)
}

func tenantListCmd(g *globalOptions) *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List tenants",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			kc, err := g.kubeClient()
			if err != nil {
				return err
			}
			names, err := namespaces.New(kc).ListTenants(ctx)
			if err != nil {
				return err
			}
			for _, n := range names {
				_, _ = fmt.Fprintln(cmd.Writer, n)
			}
			return nil
		},
	}
}

func tenantDeleteCmd(g *globalOptions) *cli.Command {
	return &cli.Command{
		Name:      "delete",
		Usage:     "Delete a tenant namespace and its cached certificates",
		ArgsUsage: "<name>",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			name := cmd.Args().First()
			if name == "" {
				return fmt.Errorf("tenant name is required")
			}

			kc, err := g.kubeClient()
			if err != nil {
				return err
			}
			if err := namespaces.New(kc).Delete(ctx, name); err != nil {
				return err
			}
			if err := os.RemoveAll(dinklecerts.TenantDir(g.certsDir, name)); err != nil {
				return fmt.Errorf("removing cached certificates for %q: %w", name, err)
			}
			_, _ = fmt.Fprintf(cmd.Writer, "deleted tenant %s\n", name)
			return nil
		},
	}
}
