package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (k *KubeClient) EnsureNamespace(ctx context.Context, namespace, owner string) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": k.serviceAccountName,
				"dink.io/owner":                owner,
			},
			Annotations: map[string]string{
				"dink.io/created-by": k.serviceAccountName,
			},
		},
	}

	_, err := k.client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating namespace %q: %w", namespace, err)
		}
		// Namespace already exists — still ensure RBAC is present.
	}

	if err := k.EnsureRBAC(ctx, namespace); err != nil {
		return fmt.Errorf("bootstrapping RBAC in namespace %q: %w", namespace, err)
	}

	return nil
}
