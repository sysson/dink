package ocistore

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/sysson/dink/pkg/ocistore/blobstore"
	"github.com/sysson/dink/pkg/ocistore/kv/boltkv"
	"github.com/sysson/dink/pkg/ocistore/kvmeta"
)

func TestBlobDeleteAndGarbageCollection(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	contentStore, err := blobstore.Open(ctx, "file://"+filepath.ToSlash(filepath.Join(dir, "blobs"))+"?create_dir=true")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = contentStore.Close() }()
	metadataStore, err := kvmeta.Open(ctx, boltkv.Config{Path: filepath.Join(dir, "metadata.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = metadataStore.Close() }()

	registry, err := New(contentStore, metadataStore)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("garbage collect me")
	digest := ocidigest.FromBytes(content)
	descriptor, err := registry.PushBlob(ctx, "team/app", oci.Descriptor{
		Digest: digest,
		Size:   int64(len(content)),
	}, bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.MediaType != "application/octet-stream" {
		t.Fatalf("blob media type = %q, want application/octet-stream", descriptor.MediaType)
	}
	if err := registry.DeleteBlob(ctx, "team/app", digest); err != nil {
		t.Fatal(err)
	}
	if err := registry.CollectGarbage(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := contentStore.Size(ctx, digest); !errors.Is(err, blobstore.ErrObjectUnknown) {
		t.Fatalf("content size error = %v, want ErrObjectUnknown", err)
	}
	if repositories, err := metadataStore.Repositories(ctx, "", 10); err != nil || len(repositories) != 0 {
		t.Fatalf("repositories after deleting final blob = %v, %v; want none", repositories, err)
	}
	if _, err := registry.ResolveBlob(ctx, "team/app", digest); !errors.Is(err, oci.ErrNameUnknown) {
		t.Fatalf("ResolveBlob error = %v, want ErrNameUnknown", err)
	}
}
