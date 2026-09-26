package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/sysson/dink/core/registry/backend/blobstore"
	"github.com/sysson/dink/core/registry/backend/kv/boltkv"
	"github.com/sysson/dink/core/registry/backend/kvmeta"
	"github.com/sysson/dink/core/registry/ocibackend"
)

func TestPersistentBlobUploadSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	handler, closeStores := persistentHandler(t, ctx, dir)
	content := []byte("durable registry content")
	digest := ocidigest.FromBytes(content)

	start := httptest.NewRecorder()
	handler.ServeHTTP(start, httptest.NewRequest(http.MethodPost, "/v2/persist/blobs/uploads/", nil))
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status = %d: %s", start.Code, start.Body)
	}
	location := start.Header().Get("Location")

	patch := httptest.NewRecorder()
	patchRequest := httptest.NewRequest(http.MethodPatch, location, bytes.NewReader(content))
	patchRequest.Header.Set("Content-Range", "0-"+strconv.Itoa(len(content)-1))
	handler.ServeHTTP(patch, patchRequest)
	if patch.Code != http.StatusAccepted {
		t.Fatalf("patch status = %d: %s", patch.Code, patch.Body)
	}
	if err := closeStores(); err != nil {
		t.Fatalf("closing stores with an open upload: %v", err)
	}
	handler, closeStores = persistentHandler(t, ctx, dir)

	commit := httptest.NewRecorder()
	handler.ServeHTTP(commit, httptest.NewRequest(http.MethodPut, location+"?digest="+url.QueryEscape(digest.String()), nil))
	if commit.Code != http.StatusCreated {
		t.Fatalf("commit status = %d: %s", commit.Code, commit.Body)
	}
	if err := closeStores(); err != nil {
		t.Fatalf("closing initial stores: %v", err)
	}

	handler, closeStores = persistentHandler(t, ctx, dir)
	t.Cleanup(func() {
		if err := closeStores(); err != nil {
			t.Errorf("closing reopened stores: %v", err)
		}
	})

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v2/persist/blobs/"+digest.String(), nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET after restart status = %d: %s", get.Code, get.Body)
	}
	if !bytes.Equal(get.Body.Bytes(), content) {
		t.Fatalf("GET after restart body = %q, want %q", get.Body.Bytes(), content)
	}
}

func TestPersistentManifestAndTagSurviveRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	handler, closeStores := persistentHandler(t, ctx, dir)
	blobContent := []byte("image config bytes")
	blobDigest := ocidigest.FromBytes(blobContent)
	pushBlobForTest(t, handler, blobContent, blobDigest)

	manifest := struct {
		SchemaVersion int    `json:"schemaVersion"`
		MediaType     string `json:"mediaType"`
		Config        struct {
			MediaType string     `json:"mediaType"`
			Digest    oci.Digest `json:"digest"`
			Size      int64      `json:"size"`
		} `json:"config"`
		Layers []any `json:"layers"`
	}{
		SchemaVersion: 2,
		MediaType:     oci.MediaTypeImageManifest,
	}
	manifest.Config.MediaType = oci.MediaTypeImageConfig
	manifest.Config.Digest = blobDigest
	manifest.Config.Size = int64(len(blobContent))
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	putRequest := httptest.NewRequest(http.MethodPut, "/v2/persist/manifests/latest", bytes.NewReader(manifestBytes))
	putRequest.Header.Set("Content-Type", oci.MediaTypeImageManifest)
	put := httptest.NewRecorder()
	handler.ServeHTTP(put, putRequest)
	if put.Code != http.StatusCreated {
		t.Fatalf("manifest PUT status = %d: %s", put.Code, put.Body)
	}
	putStableRequest := httptest.NewRequest(http.MethodPut, "/v2/persist/manifests/stable", bytes.NewReader(manifestBytes))
	putStableRequest.Header.Set("Content-Type", oci.MediaTypeImageManifest)
	putStable := httptest.NewRecorder()
	handler.ServeHTTP(putStable, putStableRequest)
	if putStable.Code != http.StatusCreated {
		t.Fatalf("second manifest tag PUT status = %d: %s", putStable.Code, putStable.Body)
	}
	if err := closeStores(); err != nil {
		t.Fatalf("closing initial stores: %v", err)
	}

	handler, closeStores = persistentHandler(t, ctx, dir)
	t.Cleanup(func() {
		if err := closeStores(); err != nil {
			t.Errorf("closing reopened stores: %v", err)
		}
	})

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v2/persist/manifests/latest", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("manifest GET after restart status = %d: %s", get.Code, get.Body)
	}
	if !bytes.Equal(get.Body.Bytes(), manifestBytes) {
		t.Fatalf("manifest GET after restart body = %s, want %s", get.Body, manifestBytes)
	}

	tags := httptest.NewRecorder()
	handler.ServeHTTP(tags, httptest.NewRequest(http.MethodGet, "/v2/persist/tags/list", nil))
	if tags.Code != http.StatusOK {
		t.Fatalf("tags GET after restart status = %d: %s", tags.Code, tags.Body)
	}
	var tagsBody struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(tags.Body.Bytes(), &tagsBody); err != nil {
		t.Fatal(err)
	}
	if len(tagsBody.Tags) != 2 || tagsBody.Tags[0] != "latest" || tagsBody.Tags[1] != "stable" {
		t.Fatalf("tags after restart = %v, want [latest stable]", tagsBody.Tags)
	}

	pagedTags := httptest.NewRecorder()
	handler.ServeHTTP(pagedTags, httptest.NewRequest(http.MethodGet, "/v2/persist/tags/list?n=1", nil))
	if pagedTags.Code != http.StatusOK || pagedTags.Header().Get("Link") == "" {
		t.Fatalf("paginated tags status/link = %d/%q, want 200 and next link; body: %s", pagedTags.Code, pagedTags.Header().Get("Link"), pagedTags.Body)
	}
	if err := json.Unmarshal(pagedTags.Body.Bytes(), &tagsBody); err != nil {
		t.Fatal(err)
	}
	if len(tagsBody.Tags) != 1 || tagsBody.Tags[0] != "latest" {
		t.Fatalf("first tag page = %v, want [latest]", tagsBody.Tags)
	}
}

func persistentHandler(t *testing.T, ctx context.Context, dir string) (*Handler, func() error) {
	t.Helper()
	content, err := blobstore.Open(ctx, "file://"+filepath.ToSlash(filepath.Join(dir, "blobs"))+"?create_dir=true")
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := kvmeta.Open(ctx, boltkv.Config{Path: filepath.Join(dir, "metadata.db")})
	if err != nil {
		_ = content.Close()
		t.Fatal(err)
	}
	backend, err := ocibackend.New(content, metadata)
	if err != nil {
		_ = content.Close()
		_ = metadata.Close()
		t.Fatal(err)
	}
	handler, err := New(backend)
	if err != nil {
		_ = content.Close()
		_ = metadata.Close()
		t.Fatal(err)
	}
	return handler, func() error {
		return errors.Join(metadata.Close(), content.Close())
	}
}

func pushBlobForTest(t *testing.T, handler http.Handler, content []byte, digest oci.Digest) {
	t.Helper()
	start := httptest.NewRecorder()
	handler.ServeHTTP(start, httptest.NewRequest(http.MethodPost, "/v2/persist/blobs/uploads/", nil))
	if start.Code != http.StatusAccepted {
		t.Fatalf("blob upload start status = %d: %s", start.Code, start.Body)
	}
	location := start.Header().Get("Location")
	patch := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, location, bytes.NewReader(content))
	request.Header.Set("Content-Range", fmt.Sprintf("0-%d", len(content)-1))
	handler.ServeHTTP(patch, request)
	if patch.Code != http.StatusAccepted {
		t.Fatalf("blob upload patch status = %d: %s", patch.Code, patch.Body)
	}
	commit := httptest.NewRecorder()
	handler.ServeHTTP(commit, httptest.NewRequest(http.MethodPut, location+"?digest="+url.QueryEscape(digest.String()), nil))
	if commit.Code != http.StatusCreated {
		t.Fatalf("blob upload commit status = %d: %s", commit.Code, commit.Body)
	}
}

func TestPushIndexWithMissingChildren(t *testing.T) {
	ctx := context.Background()
	handler, closeStores := persistentHandler(t, ctx, t.TempDir())
	defer func() { _ = closeStores() }()

	missing := oci.Descriptor{
		MediaType: oci.MediaTypeImageManifest,
		Digest:    ocidigest.FromBytes([]byte("never pushed")),
		Size:      12,
		Platform:  &oci.Platform{OS: "linux", Architecture: "arm64"},
	}
	for _, mediaType := range []string{oci.MediaTypeImageIndex, oci.MediaTypeDockerManifestList} {
		body, err := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": mediaType, "manifests": []oci.Descriptor{missing}})
		if err != nil {
			t.Fatal(err)
		}
		put := httptest.NewRequest(http.MethodPut, "/v2/multi/manifests/latest", bytes.NewReader(body))
		put.Header.Set("Content-Type", mediaType)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, put)
		if response.Code != http.StatusCreated {
			t.Fatalf("PUT %s with a missing child = %d: %s", mediaType, response.Code, response.Body)
		}
		get := httptest.NewRecorder()
		handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v2/multi/manifests/latest", nil))
		if get.Code != http.StatusOK || !bytes.Equal(get.Body.Bytes(), body) {
			t.Fatalf("GET %s = %d, want the pushed bytes", mediaType, get.Code)
		}
	}
}
