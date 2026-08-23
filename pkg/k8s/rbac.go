package k8s

import (
	"context"
	"fmt"

	rbacv1 "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	tenantRoleName        = "dink-tenant"
	tenantRoleBindingName = "dink-tenant"
)

func (kc *KubeClient) EnsureRBAC(ctx context.Context, namespace string) error {
	if err := kc.ensureRole(ctx, namespace); err != nil {
		return err
	}
	return kc.ensureRoleBinding(ctx, namespace)
}

func (kc *KubeClient) ensureRole(ctx context.Context, namespace string) error {
	role := &rbacv1.Role{
		Name:      tenantRoleName,
		Namespace: namespace,
		Labels: map[string]string{
			"app.kubernetes.io/managed-by": kc.serviceAccountName,
			"dink.io/owner":                namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods", "pods/log", "pods/exec", "pods/attach", "pods/status"},
				Verbs:     []string{"get", "list", "watch", "create", "delete", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"services"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"persistentvolumeclaims"},
				Verbs:     []string{"get", "list", "watch", "create", "delete", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"configmaps"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"apps"},
				Resources: []string{"deployments", "daemonsets", "replicasets"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"metrics.k8s.io"},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "list"},
			},
			{
				APIGroups: []string{"networking.k8s.io"},
				Resources: []string{"networkpolicies"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
		},
	}

	_, err := kc.client.RbacV1().Roles(namespace).Create(ctx, role, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating role %q in namespace %q: %w", tenantRoleName, namespace, err)
	}
	return nil
}

func (kc *KubeClient) ensureRoleBinding(ctx context.Context, namespace string) error {
	rb := &rbacv1.RoleBinding{
		Name:      tenantRoleBindingName,
		Namespace: namespace,
		Labels: map[string]string{
			"app.kubernetes.io/managed-by": kc.serviceAccountName,
			"dink.io/owner":                namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     tenantRoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      kc.serviceAccountName,
				Namespace: kc.SystemNamespace(),
			},
		},
	}

	_, err := kc.client.RbacV1().RoleBindings(namespace).Create(ctx, rb, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating rolebinding %q in namespace %q: %w", tenantRoleBindingName, namespace, err)
	}
	return nil
}
