package auth

import "context"

// NamespaceEnsurer provisions a tenant namespace (and its RBAC) on first use.
type NamespaceEnsurer interface {
	EnsureNamespace(ctx context.Context, namespace, owner string) error
}
