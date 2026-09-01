package certs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// On-disk file names. The tls.* names match the keys of a kubernetes.io/tls
// Secret; the docker/ subdirectory matches what docker(1) expects to find
// under DOCKER_CERT_PATH.
const (
	CAcertFile     = "ca.crt"
	CAKeyFile      = "ca.key"
	ServerCertFile = "tls.crt"
	ServerKeyFile  = "tls.key"
	ClientCertFile = "client.crt"
	ClientKeyFile  = "client.key"

	DockerDir      = "docker"
	DockerCAFile   = "ca.pem"
	DockerCertFile = "cert.pem"
	DockerKeyFile  = "key.pem"
)

// ErrNoAuthority is returned by LoadAuthorityDir when dir holds no CA.
var ErrNoAuthority = errors.New("no certificate authority found")

// Save writes a bundle to dir, including the docker client layout. Private
// keys are written 0600 and the directories 0700.
func Save(dir string, b *Bundle) error {
	dockerDir := filepath.Join(dir, DockerDir)
	if err := os.MkdirAll(dockerDir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dockerDir, err)
	}
	// MkdirAll leaves pre-existing directories alone.
	for _, d := range []string{dir, dockerDir} {
		if err := os.Chmod(d, 0o700); err != nil {
			return fmt.Errorf("securing %s: %w", d, err)
		}
	}

	files := []struct {
		name string
		data []byte
		mode fs.FileMode
	}{
		{CAcertFile, b.CA.Cert, 0o644},
		{CAKeyFile, b.CA.Key, 0o600},
		{ServerCertFile, b.Server.Cert, 0o644},
		{ServerKeyFile, b.Server.Key, 0o600},
		{ClientCertFile, b.Client.Cert, 0o644},
		{ClientKeyFile, b.Client.Key, 0o600},
		{filepath.Join(DockerDir, DockerCAFile), b.CA.Cert, 0o644},
		{filepath.Join(DockerDir, DockerCertFile), b.Client.Cert, 0o644},
		{filepath.Join(DockerDir, DockerKeyFile), b.Client.Key, 0o600},
	}

	for _, f := range files {
		path := filepath.Join(dir, f.name)
		if err := os.WriteFile(path, f.data, f.mode); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
		if err := os.Chmod(path, f.mode); err != nil {
			return fmt.Errorf("securing %s: %w", path, err)
		}
	}

	return nil
}

// LoadAuthorityDir reloads the CA previously written to dir by Save, so that
// re-running generation reissues leaves without invalidating clients that
// already trust the CA. It returns ErrNoAuthority if dir holds no CA.
func LoadAuthorityDir(dir string, opts Options) (*Authority, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, CAcertFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNoAuthority
		}
		return nil, err
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, CAKeyFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNoAuthority
		}
		return nil, err
	}
	return LoadAuthority(KeyPair{Cert: certPEM, Key: keyPEM}, opts)
}
