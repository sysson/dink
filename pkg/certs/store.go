package certs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// On-disk file names. The tls.* names match the keys of a kubernetes.io/tls
// Secret; the ca/cert/key.pem names match what docker(1) expects to find
// under DOCKER_CERT_PATH.
const (
	CAcertFile     = "ca.crt"
	CAKeyFile      = "ca.key"
	ServerCertFile = "tls.crt"
	ServerKeyFile  = "tls.key"

	DockerCAFile   = "ca.pem"
	DockerCertFile = "cert.pem"
	DockerKeyFile  = "key.pem"
)

// ErrNoAuthority is returned by LoadAuthorityDir when dir holds no CA.
var ErrNoAuthority = errors.New("no certificate authority found")

// SaveCA writes only the CA certificate and key to dir.
func SaveCA(dir string, ca KeyPair) error {
	return saveFiles(dir, []fileSpec{
		{CAcertFile, ca.Cert, 0o644},
		{CAKeyFile, ca.Key, 0o600},
	})
}

// SaveServer writes only the server certificate and key to dir.
func SaveServer(dir string, server KeyPair) error {
	return saveFiles(dir, []fileSpec{
		{ServerCertFile, server.Cert, 0o644},
		{ServerKeyFile, server.Key, 0o600},
	})
}

// SaveClientDir writes a client keypair and the issuing CA certificate to dir
// using the docker(1) DOCKER_CERT_PATH layout, so dir can be pointed to
// directly via DOCKER_CERT_PATH.
func SaveClientDir(dir string, ca KeyPair, client KeyPair) error {
	return saveFiles(dir, []fileSpec{
		{DockerCAFile, ca.Cert, 0o644},
		{DockerCertFile, client.Cert, 0o644},
		{DockerKeyFile, client.Key, 0o600},
	})
}

type fileSpec struct {
	name string
	data []byte
	mode fs.FileMode
}

func saveFiles(dir string, files []fileSpec) error {
	dirs := map[string]struct{}{dir: {}}
	for _, f := range files {
		dirs[filepath.Join(dir, filepath.Dir(f.name))] = struct{}{}
	}
	for d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("creating %s: %w", d, err)
		}
		// MkdirAll leaves pre-existing directories alone.
		if err := os.Chmod(d, 0o700); err != nil {
			return fmt.Errorf("securing %s: %w", d, err)
		}
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

// LoadAuthorityDir reloads the CA previously written to dir by SaveCA, so that
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
