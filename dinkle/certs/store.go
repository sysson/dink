// Package certs resolves the on-disk locations dinkle uses to cache the CA
// and issued client certificates, on top of the certificate primitives in
// pkg/certs.
package certs

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	configDirName = "dink"
	certsDirName  = ".certs"
)

// DefaultDir returns ~/.config/dink/.certs, dinkle's default certificate
// cache. The CA and server keypair live directly in this directory; tenant
// and client certificates live under namespace/client subdirectories.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".config", configDirName, certsDirName), nil
}

// TenantDir returns the directory holding a tenant's own certificates.
func TenantDir(base, namespace string) string {
	return filepath.Join(base, namespace)
}

// ClientDir returns the directory holding a single client's certificate,
// laid out so it can be used directly as DOCKER_CERT_PATH.
func ClientDir(base, namespace, client string) string {
	return filepath.Join(base, namespace, client)
}
