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
)

// Manager creates, lists and deletes the namespaces dinkle owns.
type Manager struct {
	client kubernetes.Interface
}

func New(client kubernetes.Interface) *Manager {
	return &Manager{client: client}
}

// Ensure creates namespace if it does not already exist. It is not an error
// for the namespace to already exist.
func (m *Manager) Ensure(ctx context.Context, name string, tenant bool) error {
	labels := map[string]string{LabelManagedBy: ManagedByValue}
	if tenant {
		labels[LabelTenant] = name
	}

	ns := &corev1.Namespace{
			Name:   name,
			Labels: labels,
	}
	_, err := m.client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating namespace %q: %w", name, err)
	}
	return nil
}

// Get returns the tenant namespace, or an error if it does not exist.
func (m *Manager) Get(ctx context.Context, name string) (*corev1.Namespace, error) {
	ns, err := m.client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting namespace %q: %w", name, err)
	}
	return ns, nil
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
	if err := m.client.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting namespace %q: %w", name, err)
	}
	return nil
}
