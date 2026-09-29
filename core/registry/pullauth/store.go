// Package pullauth authenticates the nodes that pull tenant images from dinki.
// Each namespace has at most one Basic credential: the username is the
// namespace and the password a random secret, of which only a hash is stored.
// An authenticated request may only read repositories under its namespace.
package pullauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/sysson/dink/pkg/ocistore/kv"
)

// nsCredential keys credentials in the metadata kv store, apart from the
// namespaces the OCI store uses.
const nsCredential = "pull-credential"

var namespacePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// ValidNamespace reports whether namespace is a Kubernetes namespace name.
func ValidNamespace(namespace string) bool {
	return namespacePattern.MatchString(namespace)
}

// Store keeps pull credentials in a kv store.
type Store struct {
	kv  kv.Store
	now func() time.Time
}

func New(store kv.Store) *Store {
	return &Store{kv: store, now: time.Now}
}

type record struct {
	Hash     []byte
	IssuedAt time.Time
}

// Issue creates, or replaces, the credential for namespace and returns its
// password.
func (s *Store) Issue(ctx context.Context, namespace string) (string, error) {
	if !ValidNamespace(namespace) {
		return "", fmt.Errorf("invalid namespace %q", namespace)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	password := base64.RawURLEncoding.EncodeToString(secret)
	hash := sha256.Sum256([]byte(password))
	value, err := json.Marshal(record{Hash: hash[:], IssuedAt: s.now()})
	if err != nil {
		return "", err
	}
	if err := s.kv.Update(ctx, func(tx kv.Txn) error {
		return tx.Put(kv.Key(nsCredential, namespace), value)
	}); err != nil {
		return "", fmt.Errorf("storing pull credential: %w", err)
	}
	return password, nil
}

// Revoke removes the credential for namespace. It is idempotent.
func (s *Store) Revoke(ctx context.Context, namespace string) error {
	if !ValidNamespace(namespace) {
		return fmt.Errorf("invalid namespace %q", namespace)
	}
	return s.kv.Update(ctx, func(tx kv.Txn) error {
		return tx.Delete(kv.Key(nsCredential, namespace))
	})
}

// Verify reports whether password is the current credential for namespace.
func (s *Store) Verify(ctx context.Context, namespace, password string) (bool, error) {
	if !ValidNamespace(namespace) {
		return false, nil
	}
	var stored record
	err := s.kv.View(ctx, func(r kv.Reader) error {
		value, err := r.Get(ctx, kv.Key(nsCredential, namespace))
		if err != nil {
			return err
		}
		return json.Unmarshal(value, &stored)
	})
	if errors.Is(err, kv.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	hash := sha256.Sum256([]byte(password))
	return subtle.ConstantTimeCompare(hash[:], stored.Hash) == 1, nil
}
