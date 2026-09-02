// Package namespaces manages the Kubernetes namespaces dinkle provisions:
// dink's own system/default namespaces and one namespace per tenant.
package namespaces

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// LabelManagedBy marks every namespace and secret dinkle provisions.
	LabelManagedBy = "app.kubernetes.io/managed-by"
	// LabelTenant marks a namespace as a tenant namespace, holding its name.
	LabelTenant = "dink.io/tenant"

	ManagedByValue = "dinkle"

	CertFieldManager = "dinkle"
)

// Manager creates, lists and deletes the namespaces dinkle owns.
type Manager struct {
	client kubernetes.Interface
}

func New(client kubernetes.Interface) *Manager {
	return &Manager{client: client}
}

// Create creates namespace if it does not already exist.
func (m *Manager) Create(ctx context.Context, name string, tenant bool) error {
	labels := map[string]string{LabelManagedBy: ManagedByValue}
	if tenant {
		labels[LabelTenant] = name
	}

	ns := &corev1.Namespace{
		Name:   name,
		Labels: labels,
	}
	_, err := m.client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	return err
}

// Get returns the tenant namespace, or an error if it does not exist.
func (m *Manager) Get(ctx context.Context, name string) (*corev1.Namespace, error) {
	return m.client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
}

// ListTenants returns the names of every tenant namespace dinkle provisioned,
// sorted alphabetically.
func (m *Manager) ListTenants(ctx context.Context) ([]string, error) {
	list, err := m.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: LabelManagedBy + "=" + ManagedByValue + "," + LabelTenant,
	})
	if err != nil {
		return nil, fmt.Errorf("listing tenant namespaces: %w", err)
	}

	names := make([]string, 0, len(list.Items))
	for _, ns := range list.Items {
		names = append(names, ns.Name)
	}
	sort.Strings(names)
	return names, nil
}

// Delete removes a tenant namespace and everything in it.
func (m *Manager) Delete(ctx context.Context, name string) error {
	return m.client.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
}

// HasDeployments reports whether the namespace contains any Deployments.
func (m *Manager) HasDeployments(ctx context.Context, name string) (bool, error) {
	list, err := m.client.AppsV1().Deployments(name).List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("listing deployments in namespace %q: %w", name, err)
	}
	return len(list.Items) > 0, nil
}

func (m *Manager) DeleteSecret(ctx context.Context, namespace, secretName string) error {
	if err := m.client.CoreV1().Secrets(namespace).Delete(ctx, secretName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting secret %s/%s: %w", namespace, secretName, err)
	}
	return nil
}

func (m *Manager) StoreServerSecret(ctx context.Context, namespace, secretName string, data map[string][]byte) error {
	secret := &corev1.Secret{
		Name:      secretName,
		Namespace: namespace,
		Labels: map[string]string{
			LabelManagedBy:           ManagedByValue,
			"app.kubernetes.io/name": "dink",
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
	return m.storeSecret(ctx, namespace, secret)
}

func (m *Manager) StoreClientSecret(ctx context.Context, namespace, clientName string, data map[string][]byte) error {
	secretName := clientSecretName(clientName)

	secret := &corev1.Secret{
		Name:      secretName,
		Namespace: namespace,
		Labels: map[string]string{
			LabelManagedBy:   ManagedByValue,
			"dink.io/client": secretName,
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
	return m.storeSecret(ctx, namespace, secret)
}

func (m *Manager) storeSecret(ctx context.Context, namespace string, secret *corev1.Secret) error {
	secrets := m.client.CoreV1().Secrets(namespace)

	_, err := secrets.Update(ctx, secret, metav1.UpdateOptions{FieldManager: CertFieldManager})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, secret, metav1.CreateOptions{FieldManager: CertFieldManager})
	}
	if err != nil {
		return fmt.Errorf("applying secret %s/%s: %w", namespace, secret.Name, err)
	}
	return nil
}

func clientSecretName(clientName string) string {
	return "dink-client-" + clientName
}
