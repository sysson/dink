package translator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/secrets"
)

const (
	envOfLabel = "dink.io/env-of"
	// envHashAnnotation changes when plugin-resolved values change, so a service update rolls its Pods.
	envHashAnnotation = "dink.io/env-hash"
)

// resolveEnv turns Docker env into container env, resolving se:// references for the caller.
func (d *Docker) resolveEnv(ctx context.Context, id identity.Identity, name string, env []string) (secrets.Env, error) {
	resolved, err := d.secrets.Resolve(ctx, id, name, containerEnv(env))
	if errors.Is(err, secrets.ErrInvalidRef) {
		return secrets.Env{}, InvalidArgument(err)
	}
	if err != nil {
		return secrets.Env{}, Unavailable(err)
	}
	return resolved, nil
}

// applyEnvSecret stores the plugin-resolved values owned by owner, removing the Secret once none are left.
func (d *Docker) applyEnvSecret(ctx context.Context, namespace, name string, env secrets.Env, owner metav1.OwnerReference) error {
	client := d.k8s.CoreV1().Secrets(namespace)
	existing, err := client.Get(ctx, env.SecretName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if len(env.Values) == 0 {
			return nil
		}
		secret := &corev1.Secret{
			Name:            env.SecretName,
			Namespace:       namespace,
			Labels:          map[string]string{managedByLabel: managedByDink, envOfLabel: name},
			OwnerReferences: []metav1.OwnerReference{owner},
			Type:            corev1.SecretTypeOpaque,
			Data:            env.Values,
		}
		_, err = client.Create(ctx, secret, metav1.CreateOptions{})
		return kubeError(err)
	case err != nil:
		return kubeError(err)
	}

	if existing.Labels[envOfLabel] != name {
		return Conflict(fmt.Errorf("secret %s already exists and is not managed by dink", env.SecretName))
	}
	if len(env.Values) == 0 {
		err := client.Delete(ctx, existing.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &existing.UID}})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return kubeError(err)
	}
	existing.Data = env.Values
	existing.OwnerReferences = []metav1.OwnerReference{owner}
	_, err = client.Update(ctx, existing, metav1.UpdateOptions{})
	return kubeError(err)
}

func envHash(values map[string][]byte) string {
	if len(values) == 0 {
		return ""
	}
	h := sha256.New()
	for _, k := range slices.Sorted(maps.Keys(values)) {
		_, _ = fmt.Fprintf(h, "%d:%s%d:", len(k), k, len(values[k]))
		h.Write(values[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}
