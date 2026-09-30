package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/sysson/dink/cmd/dinkle/namespaces"
	"github.com/sysson/dink/core/secrets"
)

var ErrSecretKeyRequired = errors.New("secret key is required")

// maxSecretSize keeps the store well under the 1MiB Kubernetes Secret limit.
const maxSecretSize = 256 << 10

func secretCmd(o *Options) *cli.Command {
	return &cli.Command{
		Name:  "secret",
		Usage: "Manage the values tenants reference as se://k8s/<key>",
		Description: "Values are stored in the tenant's " + secrets.StoreName + " Secret. A container env\n" +
			"like PASSWORD=se://k8s/db-password then receives the stored value.",
		Commands: []*cli.Command{
			secretSetCmd(o),
			secretListCmd(o),
			secretRemoveCmd(o),
		},
	}
}

func secretSetCmd(o *Options) *cli.Command {
	var fromFile string
	return &cli.Command{
		Name:      "set",
		Usage:     "Set a value, read from --from-file or stdin",
		ArgsUsage: "<tenant> <key>",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "from-file",
				Usage:       "Read the value from this file instead of stdin",
				Destination: &fromFile,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			tenant, key, err := tenantAndKey(cmd)
			if err != nil {
				return err
			}
			value, err := readSecretValue(cmd.Root().Reader, fromFile)
			if err != nil {
				return err
			}
			return newService(o, &caOptions{}).SetSecret(ctx, tenant, key, value, cmd.Writer)
		},
	}
}

func secretListCmd(o *Options) *cli.Command {
	return &cli.Command{
		Name:      "list",
		Usage:     "List a tenant's keys, never their values",
		ArgsUsage: "<tenant>",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			tenant := cmd.Args().First()
			if err := requireString(tenant, ErrTenantNameRequired); err != nil {
				return err
			}
			return newService(o, &caOptions{}).ListSecrets(ctx, tenant, cmd.Writer)
		},
	}
}

func secretRemoveCmd(o *Options) *cli.Command {
	return &cli.Command{
		Name:      "remove",
		Usage:     "Remove a value",
		ArgsUsage: "<tenant> <key>",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			tenant, key, err := tenantAndKey(cmd)
			if err != nil {
				return err
			}
			return newService(o, &caOptions{}).RemoveSecret(ctx, tenant, key, cmd.Writer)
		},
	}
}

func tenantAndKey(cmd *cli.Command) (string, string, error) {
	tenant, key := cmd.Args().Get(0), cmd.Args().Get(1)
	if err := requireString(tenant, ErrTenantNameRequired); err != nil {
		return "", "", err
	}
	if err := requireString(key, ErrSecretKeyRequired); err != nil {
		return "", "", err
	}
	if errs := validation.IsConfigMapKey(key); len(errs) > 0 {
		return "", "", fmt.Errorf("invalid key %q: %s", key, strings.Join(errs, "; "))
	}
	return tenant, key, nil
}

// readSecretValue keeps values off the command line, where they would end up in shell history.
func readSecretValue(stdin io.Reader, fromFile string) ([]byte, error) {
	r := stdin
	if fromFile != "" {
		f, err := os.Open(fromFile)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	if r == nil {
		r = os.Stdin
	}
	value, err := io.ReadAll(io.LimitReader(r, maxSecretSize+1))
	if err != nil {
		return nil, err
	}
	if len(value) > maxSecretSize {
		return nil, fmt.Errorf("value exceeds %d bytes", maxSecretSize)
	}
	if fromFile == "" {
		value = []byte(strings.TrimSuffix(string(value), "\n"))
	}
	return value, nil
}

func (s *Service) secretStore(ctx context.Context, tenant string) (*corev1.Secret, error) {
	kc, err := s.options.kubeClient(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := namespaces.New(kc).Get(ctx, tenant); err != nil {
		return nil, err
	}
	store, err := kc.CoreV1().Secrets(tenant).Get(ctx, secrets.StoreName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if store.Labels[secrets.StoreLabel] != "true" {
		return nil, fmt.Errorf("secret %s/%s exists but is not a dink secret store", tenant, secrets.StoreName)
	}
	return store, nil
}

func (s *Service) SetSecret(ctx context.Context, tenant, key string, value []byte, out io.Writer) error {
	store, err := s.secretStore(ctx, tenant)
	if err != nil {
		return err
	}
	kc, err := s.options.kubeClient(ctx)
	if err != nil {
		return err
	}
	if store == nil {
		_, err = kc.CoreV1().Secrets(tenant).Create(ctx, &corev1.Secret{
			Name:      secrets.StoreName,
			Namespace: tenant,
			Labels: map[string]string{
				secrets.StoreLabel:        "true",
				namespaces.LabelManagedBy: namespaces.ManagedByValue,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{key: value},
		}, metav1.CreateOptions{})
	} else {
		if store.Data == nil {
			store.Data = map[string][]byte{}
		}
		store.Data[key] = value
		_, err = kc.CoreV1().Secrets(tenant).Update(ctx, store, metav1.UpdateOptions{})
	}
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "set se://k8s/%s for tenant %s\n", key, tenant)
	return nil
}

func (s *Service) ListSecrets(ctx context.Context, tenant string, out io.Writer) error {
	store, err := s.secretStore(ctx, tenant)
	if err != nil || store == nil {
		return err
	}
	keys := make([]string, 0, len(store.Data))
	for k := range store.Data {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		_, _ = fmt.Fprintf(out, "se://k8s/%s\n", k)
	}
	return nil
}

func (s *Service) RemoveSecret(ctx context.Context, tenant, key string, out io.Writer) error {
	store, err := s.secretStore(ctx, tenant)
	if err != nil {
		return err
	}
	if store == nil || store.Data[key] == nil {
		return fmt.Errorf("se://k8s/%s is not set for tenant %s", key, tenant)
	}
	delete(store.Data, key)
	kc, err := s.options.kubeClient(ctx)
	if err != nil {
		return err
	}
	if _, err := kc.CoreV1().Secrets(tenant).Update(ctx, store, metav1.UpdateOptions{}); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "removed se://k8s/%s for tenant %s\n", key, tenant)
	return nil
}
