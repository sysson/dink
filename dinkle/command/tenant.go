package command

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/sysson/dink/dinkle/namespaces"
	"github.com/sysson/dink/dinkle/store"
	"github.com/urfave/cli/v3"
)

// defaultTenantClient is the client certificate every tenant is issued at
// creation time, before any additional `dinkle client create` calls.
const defaultTenantClient = "default"

func tenantCmd(o *Options) *cli.Command {
	return &cli.Command{
		Name:  "tenant",
		Usage: "Create, list and delete tenants",
		Commands: []*cli.Command{
			tenantCreateCmd(o),
			tenantListCmd(o),
			tenantDeleteCmd(o),
		},
	}
}

func tenantCreateCmd(o *Options) *cli.Command {
	return &cli.Command{
		Name:      "create",
		Usage:     "Create a tenant namespace and issue its default client certificate",
		ArgsUsage: "<name>",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			name := cmd.Args().First()
			if name == "" {
				return fmt.Errorf("tenant name is required")
			}
			return tenantCreate(ctx, o, name, cmd.Writer)
		},
	}
}

func tenantCreate(ctx context.Context, o *Options, name string, out io.Writer) error {
	ca, err := loadAuthority(o.certsDir)
	if err != nil {
		return err
	}

	kc, err := o.kubeClient(ctx)
	if err != nil {
		return err
	}

	nsManager := namespaces.New(kc)

	if err := nsManager.Ensure(ctx, name, true); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "ensured tenant namespace %s\n", name)

	return issueClientCert(ctx, nsManager, ca, name, defaultTenantClient, o.certsDir)
}

func tenantListCmd(o *Options) *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List tenants",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			kc, err := o.kubeClient(ctx)
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

func tenantDeleteCmd(o *Options) *cli.Command {
	var force bool
	return &cli.Command{
		Name:      "delete",
		Usage:     "Delete a tenant namespace and its cached certificates",
		ArgsUsage: "<name>",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:        "force",
				Usage:       "Delete the tenant even if it has running deployments",
				Destination: &force,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			name := cmd.Args().First()
			if name == "" {
				return fmt.Errorf("tenant name is required")
			}

			kc, err := o.kubeClient(ctx)
			if err != nil {
				return err
			}

			nsManager := namespaces.New(kc)
			if !force {
				has, err := nsManager.HasDeployments(ctx, name)
				if err != nil {
					return err
				}
				if has {
					return fmt.Errorf("tenant %q has running deployments; use --force to delete anyway", name)
				}
			}

			if err := nsManager.Delete(ctx, name); err != nil {
				return err
			}
			if err := os.RemoveAll(store.TenantDir(o.certsDir, name)); err != nil {
				return fmt.Errorf("removing cached certificates for %q: %w", name, err)
			}
			_, _ = fmt.Fprintf(cmd.Writer, "deleted tenant %s\n", name)
			return nil
		},
	}
}
