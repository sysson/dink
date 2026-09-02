package command

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/sysson/dink/dinkle/namespaces"
	"github.com/sysson/dink/dinkle/store"
	"github.com/urfave/cli/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

			ca, err := loadAuthority(o.certsDir)
			if err != nil {
				return err
			}

			kc, err := o.kubeClient(ctx)
			if err != nil {
				return err
			}
			if _, err := namespaces.New(kc).Get(ctx, namespace); err != nil {
				return fmt.Errorf("tenant namespace %q does not exist; create it with `dinkle tenant create %s` first: %w", namespace, namespace, err)
			}

			return issueClientCert(ctx, namespaces.New(kc), ca, namespace, clientName, o.certsDir)
		},
	}
}

func clientListCmd(o *Options) *cli.Command {
	return &cli.Command{
		Name:      "list",
		Usage:     "List the client certificates cached for a tenant namespace",
		ArgsUsage: "<namespace>",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			namespace := cmd.Args().First()
			if namespace == "" {
				return fmt.Errorf("namespace is required")
			}

			entries, err := os.ReadDir(store.TenantDir(o.certsDir, namespace))
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
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

			kc, err := o.kubeClient(ctx)
			if err != nil {
				return err
			}
			secretName := clientSecretName(clientName)
			if err := kc.CoreV1().Secrets(namespace).Delete(ctx, secretName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("deleting secret %s/%s: %w", namespace, secretName, err)
			}
			if err := os.RemoveAll(store.ClientDir(o.certsDir, namespace, clientName)); err != nil {
				return fmt.Errorf("removing cached certificate for %s/%s: %w", namespace, clientName, err)
			}
			_, _ = fmt.Fprintf(cmd.Writer, "deleted client %s/%s\n", namespace, clientName)
			return nil
		},
	}
}

func namespaceAndClientArgs(cmd *cli.Command) (namespace, clientName string, err error) {
	args := cmd.Args()
	namespace = args.Get(0)
	clientName = args.Get(1)
	if namespace == "" || clientName == "" {
		return "", "", fmt.Errorf("namespace and client name are required")
	}
	return namespace, clientName, nil
}
