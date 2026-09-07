package store

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/sysson/syskit/pki"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// SecretStore is a pki.Store backed by Kubernetes Secrets in a single
// namespace: the CA lives in a fixed Secret, and each leaf lives in its own
// Secret, named by the caller.
type SecretStore struct {
	Client       kubernetes.Interface
	Namespace    string
	CASecretName string

	// Labels are applied to every Secret this store writes. LeafLabelKey is
	// additionally set to "true" on leaf Secrets, so ListLeaves can select
	// them without also matching the CA Secret.
	Labels       map[string]string
	LeafLabelKey string
	FieldManager string

	// Data key names within each Secret. Defaults to the kubernetes.io/tls
	// Secret convention.
	CACertKey string
	CAKeyKey  string
	CertKey   string
	KeyKey    string
}

// New returns a Store scoped to namespace, using generic
// defaults; override the exported fields to match a different convention.
func NewSecretStore(client kubernetes.Interface, namespace, caSecretName string) *SecretStore {
	return &SecretStore{
		Client:       client,
		Namespace:    namespace,
		CASecretName: caSecretName,
		LeafLabelKey: "pki.io/leaf",
		FieldManager: "pki",
		CACertKey:    "ca.crt",
		CAKeyKey:     "ca.key",
		CertKey:      "tls.crt",
		KeyKey:       "tls.key",
	}
}

func (s *SecretStore) SaveCA(ctx context.Context, ca pki.KeyPair) error {
	return s.apply(ctx, s.CASecretName, s.labels(), map[string][]byte{s.CACertKey: ca.Cert, s.CAKeyKey: ca.Key})
}

func (s *SecretStore) LoadCA(ctx context.Context) (pki.KeyPair, error) {
	secret, err := s.get(ctx, s.CASecretName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return pki.KeyPair{}, pki.ErrNoAuthority
		}
		return pki.KeyPair{}, err
	}
	return pki.KeyPair{Cert: secret.Data[s.CACertKey], Key: secret.Data[s.CAKeyKey]}, nil
}

func (s *SecretStore) SaveLeaf(ctx context.Context, name string, ca, leaf pki.KeyPair) error {
	data := map[string][]byte{s.CertKey: leaf.Cert, s.KeyKey: leaf.Key}
	if len(ca.Cert) > 0 {
		data[s.CACertKey] = ca.Cert
	}
	labels := s.labels()
	if s.LeafLabelKey != "" {
		labels[s.LeafLabelKey] = "true"
	}
	return s.apply(ctx, name, labels, data)
}

func (s *SecretStore) LoadLeaf(ctx context.Context, name string) (ca, leaf pki.KeyPair, err error) {
	secret, err := s.get(ctx, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return pki.KeyPair{}, pki.KeyPair{}, errors.New("no certificate found")
		}
		return pki.KeyPair{}, pki.KeyPair{}, err
	}
	leaf = pki.KeyPair{Cert: secret.Data[s.CertKey], Key: secret.Data[s.KeyKey]}
	ca = pki.KeyPair{Cert: secret.Data[s.CACertKey]}
	return ca, leaf, nil
}

func (s *SecretStore) DeleteLeaf(ctx context.Context, name string) error {
	if err := s.Client.CoreV1().Secrets(s.Namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting secret %s/%s: %w", s.Namespace, name, err)
	}
	return nil
}

// ListLeaves returns the names of every leaf Secret in the namespace, sorted
// alphabetically.
func (s *SecretStore) ListLeaves(ctx context.Context) ([]string, error) {
	selector := labelSelector(s.labels())
	if s.LeafLabelKey != "" {
		if selector != "" {
			selector += ","
		}
		selector += s.LeafLabelKey
	}
	list, err := s.Client.CoreV1().Secrets(s.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("listing secrets in namespace %q: %w", s.Namespace, err)
	}
	names := make([]string, 0, len(list.Items))
	for _, secret := range list.Items {
		names = append(names, secret.Name)
	}
	sort.Strings(names)
	return names, nil
}

func (s *SecretStore) labels() map[string]string {
	labels := make(map[string]string, len(s.Labels))
	maps.Copy(labels, s.Labels)
	return labels
}

func labelSelector(labels map[string]string) string {
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func (s *SecretStore) get(ctx context.Context, name string) (*corev1.Secret, error) {
	secret, err := s.Client.CoreV1().Secrets(s.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting secret %s/%s: %w", s.Namespace, name, err)
	}
	return secret, nil
}

func (s *SecretStore) apply(ctx context.Context, name string, labels map[string]string, data map[string][]byte) error {

	secret := &corev1.Secret{
		Name:      name,
		Namespace: s.Namespace,
		Labels:    labels,
		Type:      corev1.SecretTypeOpaque,
		Data:      data,
	}

	secrets := s.Client.CoreV1().Secrets(s.Namespace)

	_, err := secrets.Update(ctx, secret, metav1.UpdateOptions{FieldManager: s.FieldManager})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, secret, metav1.CreateOptions{FieldManager: s.FieldManager})
	}
	if err != nil {
		return fmt.Errorf("applying secret %s/%s: %w", s.Namespace, name, err)
	}
	return nil
}
