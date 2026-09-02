package command

import (
	"context"

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
			if err := requireString(name, ErrTenantNameRequired); err != nil {
				return err
			}
			return newService(o).CreateTenant(ctx, name, cmd.Writer)
		},
	}
}

func tenantListCmd(o *Options) *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List tenants",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return newService(o).ListTenants(ctx, cmd.Writer)
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
			if err := requireString(name, ErrTenantNameRequired); err != nil {
				return err
			}
			return newService(o).DeleteTenant(ctx, name, force, cmd.Writer)
		},
	}
}
