package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/sysson/syskit/pki"
)

// FileStore is a Store backed by a directory: the CA lives directly in Dir,
// and each leaf lives in its own Dir/name subdirectory.
type FileStore struct {
	Dir string

	// CACertFile and CAKeyFile name the CA's files within Dir.
	CACertFile string
	CAKeyFile  string

	// LeafCertFile and LeafKeyFile name a leaf's files within its own
	// subdirectory. LeafCACertFile additionally saves the issuing CA
	// certificate alongside the leaf if set, e.g. for consumers that expect
	// a CA/cert/key bundle in one directory.
	LeafCertFile   string
	LeafKeyFile    string
	LeafCACertFile string
}

// New returns a FileStore rooted at dir, using generic file names
// (ca.crt/ca.key for the CA, tls.crt/tls.key for each leaf) matching the
// kubernetes.io/tls Secret convention. Override the exported fields to use
// different names, e.g. docker(1)'s DOCKER_CERT_PATH layout.
func NewFileStore(dir string) *FileStore {
	return &FileStore{
		Dir:          dir,
		CACertFile:   "ca.crt",
		CAKeyFile:    "ca.key",
		LeafCertFile: "tls.crt",
		LeafKeyFile:  "tls.key",
	}
}

func (s *FileStore) SaveCA(_ context.Context, ca pki.KeyPair) error {
	return saveFiles(s.Dir, []fileSpec{
		{s.CACertFile, ca.Cert, 0o644},
		{s.CAKeyFile, ca.Key, 0o600},
	})
}

func (s *FileStore) LoadCA(_ context.Context) (pki.KeyPair, error) {
	cert, err := readFile(filepath.Join(s.Dir, s.CACertFile), pki.ErrNoAuthority)
	if err != nil {
		return pki.KeyPair{}, err
	}
	key, err := readFile(filepath.Join(s.Dir, s.CAKeyFile), pki.ErrNoAuthority)
	if err != nil {
		return pki.KeyPair{}, err
	}
	return pki.KeyPair{Cert: cert, Key: key}, nil
}

func (s *FileStore) SaveLeaf(_ context.Context, name string, ca, leaf pki.KeyPair) error {
	files := []fileSpec{
		{s.LeafCertFile, leaf.Cert, 0o644},
		{s.LeafKeyFile, leaf.Key, 0o600},
	}
	if s.LeafCACertFile != "" && len(ca.Cert) > 0 {
		files = append(files, fileSpec{s.LeafCACertFile, ca.Cert, 0o644})
	}
	return saveFiles(s.leafDir(name), files)
}

func (s *FileStore) LoadLeaf(_ context.Context, name string) (ca, leaf pki.KeyPair, err error) {
	dir := s.leafDir(name)
	leaf.Cert, err = readFile(filepath.Join(dir, s.LeafCertFile), errors.New("no certificate found"))
	if err != nil {
		return pki.KeyPair{}, pki.KeyPair{}, err
	}
	leaf.Key, err = readFile(filepath.Join(dir, s.LeafKeyFile), errors.New("no certificate found"))
	if err != nil {
		return pki.KeyPair{}, pki.KeyPair{}, err
	}
	if s.LeafCACertFile != "" {
		// The companion CA certificate is optional: ignore its absence.
		ca.Cert, _ = os.ReadFile(filepath.Join(dir, s.LeafCACertFile))
	}
	return ca, leaf, nil
}

func (s *FileStore) DeleteLeaf(_ context.Context, name string) error {
	if err := os.RemoveAll(s.leafDir(name)); err != nil {
		return fmt.Errorf("removing %s: %w", s.leafDir(name), err)
	}
	return nil
}

// ListLeaves returns the names of every leaf subdirectory in Dir, sorted
// alphabetically. It returns an empty list, not an error, if Dir doesn't
// exist yet.
func (s *FileStore) ListLeaves(_ context.Context) ([]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing %s: %w", s.Dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (s *FileStore) leafDir(name string) string { return filepath.Join(s.Dir, name) }

func readFile(path string, notFound error) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, notFound
		}
		return nil, err
	}
	return data, nil
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
