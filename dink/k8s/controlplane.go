package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	defaultControlNamespace      = "system"
	defaultControlServiceAccount = "dink"
)

func (kc *KubeClient) ensureControlPlane(ctx context.Context) error {
	if err := kc.EnsureNamespace(ctx, kc.SystemNamespace(), kc.serviceAccountName); err != nil {
		return err
	}
	if err := kc.ensureServiceAccount(ctx, kc.client, kc.SystemNamespace(), kc.serviceAccountName); err != nil {
		return err
	}
	return nil
}

func (kc *KubeClient) ensureServiceAccount(ctx context.Context, client kubernetes.Interface, namespace, serviceAccount string) error {
	sa := &corev1.ServiceAccount{
		Name:      serviceAccount,
		Namespace: namespace,
		Labels: map[string]string{
			"app.kubernetes.io/name":       serviceAccount,
			"app.kubernetes.io/managed-by": serviceAccount,
		},
	}

	_, err := client.CoreV1().ServiceAccounts(namespace).Create(ctx, sa, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating control serviceaccount %q in namespace %q: %w", serviceAccount, namespace, err)
	}
	return nil
}
