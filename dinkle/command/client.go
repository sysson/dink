package command

import (
	"context"
	"fmt"

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
			clientInfoCmd(o),
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
			return newService(o, &caOptions{}).CreateClient(ctx, namespace, clientName, cmd.Writer)
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

			names, err := clientStore(o.certsDir, namespace).ListLeaves(ctx)
			if err != nil {
				return fmt.Errorf("listing clients for %q: %w", namespace, err)
			}
			for _, name := range names {
				_, _ = fmt.Fprintln(cmd.Writer, name)
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

			return newService(o, &caOptions{}).DeleteClient(ctx, namespace, clientName, cmd.Writer)
		},
	}
}

func clientInfoCmd(o *Options) *cli.Command {
	return &cli.Command{
		Name:      "info",
		Usage:     "Show a client certificate's local and cluster state, and whether they match",
		ArgsUsage: "<namespace> <client>",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			namespace, clientName, err := namespaceAndClientArgs(cmd)
			if err != nil {
				return err
			}
			return newService(o, &caOptions{}).ClientInfo(ctx, namespace, clientName, cmd.Writer)
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
