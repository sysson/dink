package command

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"path/filepath"
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	dinkiconfig "github.com/sysson/dink/cmd/dinki/config"
	"github.com/sysson/dink/core/registry/backend/blobstore"
	"github.com/sysson/dink/core/registry/backend/kv/boltkv"
	"github.com/sysson/dink/core/registry/backend/kv/drivers"
	"github.com/sysson/dink/core/registry/backend/kv/memkv"
)

func TestNewBackendOpensPersistentConfiguredStores(t *testing.T) {
	dir := t.TempDir()
	cfg := dinkiconfig.Default()
	cfg.Storage = blobstore.Config{File: &blobstore.FileConfig{Path: filepath.Join(dir, "blobs")}}
	cfg.Metadata = drivers.Config{BBolt: &boltkv.Config{Path: filepath.Join(dir, "metadata.db")}}

	backend, err := newBackend(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("persisted by dinki backend factory")
	digest := ocidigest.FromBytes(content)
	if _, err := backend.registry.PushBlob(context.Background(), "test/app", oci.Descriptor{
		Digest: digest,
		Size:   int64(len(content)),
	}, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	backend, err = newBackend(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := backend.Close(); err != nil {
			t.Errorf("closing reopened backend: %v", err)
		}
	}()
	reader, err := backend.registry.GetBlob(context.Background(), "test/app", digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	var got bytes.Buffer
	if _, err := got.ReadFrom(reader); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), content) {
		t.Fatalf("persisted blob = %q, want %q", got.Bytes(), content)
	}
}

func TestNewBackendRejectsInvalidDrivers(t *testing.T) {
	file := blobstore.Config{File: &blobstore.FileConfig{Path: filepath.Join(t.TempDir(), "blobs")}}
	mem := drivers.Config{Mem: &memkv.Config{}}
	for name, cfg := range map[string]dinkiconfig.Config{
		"no storage":        {Metadata: mem},
		"no metadata":       {Storage: file},
		"two metadata":      {Storage: file, Metadata: drivers.Config{Mem: &memkv.Config{}, BBolt: &boltkv.Config{Path: "/tmp/db"}}},
		"relative bbolt":    {Storage: file, Metadata: drivers.Config{BBolt: &boltkv.Config{Path: "relative.db"}}},
		"invalid s3 bucket": {Storage: blobstore.Config{S3: &blobstore.S3Config{Bucket: "_"}}, Metadata: mem},
	} {
		if backend, err := newBackend(context.Background(), cfg); err == nil {
			_ = backend.Close()
			t.Errorf("%s: newBackend succeeded, want error", name)
		}
	}
}

func TestNewBackendSupportsMemory(t *testing.T) {
	backend, err := newBackend(context.Background(), dinkiconfig.Config{Storage: blobstore.Config{Mem: &blobstore.MemConfig{}}, Metadata: drivers.Config{Mem: &memkv.Config{}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyAPIClientRequiresDinkSubject(t *testing.T) {
	verify := verifyAPIClient("dink-system", "dink.dink-system.svc")
	state := func(organization, commonName string) tls.ConnectionState {
		return tls.ConnectionState{PeerCertificates: []*x509.Certificate{{
			Subject: pkix.Name{Organization: []string{organization}, CommonName: commonName},
		}}}
	}
	if err := verify(state("dink-system", "dink.dink-system.svc")); err != nil {
		t.Fatalf("dink certificate rejected: %v", err)
	}
	for name, peer := range map[string]tls.ConnectionState{
		"tenant":        state("team-a", "alice"),
		"spoofed CN":    state("team-a", "dink.dink-system.svc"),
		"spoofed org":   state("dink-system", "alice"),
		"no peer certs": {},
	} {
		if err := verify(peer); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
