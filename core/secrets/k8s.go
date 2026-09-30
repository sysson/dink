package secrets

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	builtinProvider = "k8s"
	// StoreName is the Secret in each tenant namespace that se://k8s/<key> reads from.
	StoreName = "dink-secrets"
	// StoreLabel must be "true" on the store, so a docker secret with the same name is never read.
	StoreLabel = "dink.io/secret-store"
)

// resolveBuiltin points each var at its key in the tenant's store; dink never reads the value itself.
func (r *Resolver) resolveBuiltin(ctx context.Context, namespace string, env *Env, refs []ref) error {
	store, err := r.k8s.CoreV1().Secrets(namespace).Get(ctx, StoreName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) || (err == nil && store.Labels[StoreLabel] != "true") {
		return fmt.Errorf("%w: namespace %s has no %s secret store", ErrInvalidRef, namespace, builtinProvider)
	}
	if err != nil {
		return fmt.Errorf("reading %s secret store: %w", builtinProvider, err)
	}
	for _, ref := range refs {
		if _, ok := store.Data[ref.key]; !ok {
			return fmt.Errorf("%w: %s%s/%s not found", ErrInvalidRef, Scheme, builtinProvider, ref.key)
		}
		env.Vars[ref.index].Value = ""
		env.Vars[ref.index].ValueFrom = secretKeyRef(StoreName, ref.key)
	}
	return nil
}
