package command

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/sysson/dink/dinkle/store"
	"github.com/urfave/cli/v3"
)

func clientCmd(o *Options) *cli.Command {
	return &cli.Command{
		Name:  "client",
		Usage: "Create, list and delete client certificates within a tenant namespace",
		Commands: []*cli.Command{
			clientCreateCmd(o),
			clientListCmd(o),
			clientDeleteCmd(o),
		},
	}
}

func clientCreateCmd(o *Options) *cli.Command {
	return &cli.Command{
		Name:      "create",
		Usage:     "Issue a client certificate scoped to a tenant namespace",
		ArgsUsage: "<namespace> <client>",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			namespace, clientName, err := namespaceAndClientArgs(cmd)
			if err != nil {
				return err
			}

			return newService(o).CreateClient(ctx, namespace, clientName, cmd.Writer)
		},
	}
}

func clientListCmd(o *Options) *cli.Command {
	var namespace string
	return &cli.Command{
		Name:      "list",
		Usage:     "List the client certificates cached for a tenant namespace",
		ArgsUsage: "<namespace>",
		Arguments: []cli.Argument{
			&cli.StringArg{
				Name:        "namespace",
				Destination: &namespace,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {

			if err := requireString(namespace, ErrNamespaceRequired); err != nil {
				return err
			}

			entries, err := os.ReadDir(store.TenantDir(o.certsDir, namespace))
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return fmt.Errorf("tenant %q not found", namespace)
				}
				return fmt.Errorf("listing clients for %q: %w", namespace, err)
			}
			for _, e := range entries {
				if e.IsDir() {
					_, _ = fmt.Fprintln(cmd.Writer, e.Name())
				}
			}
			return nil
		},
	}
}

func clientDeleteCmd(o *Options) *cli.Command {
	return &cli.Command{
		Name:      "delete",
		Usage:     "Delete a client certificate and its Secret",
		ArgsUsage: "<namespace> <client>",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			namespace, clientName, err := namespaceAndClientArgs(cmd)
			if err != nil {
				return err
			}

			return newService(o).DeleteClient(ctx, namespace, clientName, cmd.Writer)
		},
	}
}

func namespaceAndClientArgs(cmd *cli.Command) (namespace, clientName string, err error) {
	args := cmd.Args()
	namespace = args.Get(0)
	clientName = args.Get(1)
	if err := requireString(namespace, ErrNamespaceRequired); err != nil {
		return "", "", err
	}
	if err := requireString(clientName, ErrClientNameRequired); err != nil {
		return "", "", err
	}
	return namespace, clientName, nil
}
