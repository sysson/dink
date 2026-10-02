package pullauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/docker/oci/ociref"
	"github.com/sysson/ocistore/kv"
)

const buildCredential = "build-credential"
const BuildCredentialLifetime = time.Hour

type buildRecord struct {
	Hash         []byte
	Namespace    string
	Repositories []string
	ExpiresAt    time.Time
}

func (s *Store) IssueBuild(ctx context.Context, namespace string, repositories []string) (string, string, error) {
	if !ValidNamespace(namespace) || len(repositories) == 0 {
		return "", "", fmt.Errorf("build credential requires a namespace and repositories")
	}
	for _, repo := range repositories {
		if !strings.HasPrefix(repo, namespace+"/") || !ociref.IsValidRepository(repo) {
			return "", "", fmt.Errorf("build repository %q is outside namespace %q", repo, namespace)
		}
	}
	username := "build_" + rand.Text()
	password := rand.Text()
	hash := sha256.Sum256([]byte(password))
	value, err := json.Marshal(buildRecord{
		Hash: hash[:], Namespace: namespace, Repositories: slices.Clone(repositories),
		ExpiresAt: s.now().Add(BuildCredentialLifetime),
	})
	if err != nil {
		return "", "", err
	}
	if err := s.kv.Update(ctx, func(tx kv.Txn) error {
		var expiredKeys []string
		if err := tx.Scan(ctx, kv.Prefix(buildCredential), "", func(key string, value []byte) (bool, error) {
			var expired buildRecord
			if err := json.Unmarshal(value, &expired); err != nil {
				return false, err
			}
			if !s.now().Before(expired.ExpiresAt) {
				expiredKeys = append(expiredKeys, key)
			}
			return true, nil
		}); err != nil {
			return err
		}
		for _, key := range expiredKeys {
			if err := tx.Delete(key); err != nil {
				return err
			}
		}
		return tx.Put(kv.Key(buildCredential, username), value)
	}); err != nil {
		return "", "", fmt.Errorf("storing build credential: %w", err)
	}
	return username, password, nil
}

func (s *Store) VerifyBuild(ctx context.Context, username, password string) (string, []string, error) {
	var stored buildRecord
	err := s.kv.View(ctx, func(r kv.Reader) error {
		value, err := r.Get(ctx, kv.Key(buildCredential, username))
		if err != nil {
			return err
		}
		return json.Unmarshal(value, &stored)
	})
	if errors.Is(err, kv.ErrNotFound) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	hash := sha256.Sum256([]byte(password))
	if !s.now().Before(stored.ExpiresAt) || subtle.ConstantTimeCompare(hash[:], stored.Hash) != 1 {
		return "", nil, nil
	}
	return stored.Namespace, stored.Repositories, nil
}

func (s *Store) RevokeBuild(ctx context.Context, namespace, username string) error {
	if !ValidNamespace(namespace) || !strings.HasPrefix(username, "build_") || !kv.ValidPart(username) {
		return fmt.Errorf("invalid build credential identity")
	}
	return s.kv.Update(ctx, func(tx kv.Txn) error {
		value, err := tx.Get(ctx, kv.Key(buildCredential, username))
		if errors.Is(err, kv.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var stored buildRecord
		if err := json.Unmarshal(value, &stored); err != nil {
			return err
		}
		if stored.Namespace != namespace {
			return fmt.Errorf("build credential belongs to another namespace")
		}
		return tx.Delete(kv.Key(buildCredential, username))
	})
}

type buildScopeKey struct{}

func withBuildScope(ctx context.Context, repositories []string) context.Context {
	return context.WithValue(ctx, buildScopeKey{}, slices.Clone(repositories))
}

func canWrite(ctx context.Context, repository string) bool {
	repositories, _ := ctx.Value(buildScopeKey{}).([]string)
	return slices.Contains(repositories, repository)
}
