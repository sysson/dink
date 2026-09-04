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
	// LabelLeaf marks a Secret as holding a leaf certificate, so ListLeaves
	// can find every leaf in a namespace without also matching the CA Secret.
	LabelLeaf = "dink.io/leaf"

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

// Create creates a namespace owned by dinkle.
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

// Ensure creates a non-tenant namespace unless it already exists.
func (m *Manager) Ensure(ctx context.Context, name string) error {
	if err := m.Create(ctx, name, false); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// Get returns a dinkle-managed tenant namespace, or an error if it does not
// exist or is not a tenant namespace owned by dinkle.
func (m *Manager) Get(ctx context.Context, name string) (*corev1.Namespace, error) {
	ns, err := m.client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if ns.Labels[LabelManagedBy] != ManagedByValue || ns.Labels[LabelTenant] != name {
		return nil, fmt.Errorf("namespace %q is not a dinkle-managed tenant", name)
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
	if _, err := m.Get(ctx, name); err != nil {
		return err
	}
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
