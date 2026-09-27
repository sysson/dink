package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ocimem"
)

type testBackend struct {
	*oci.Funcs
}

var _ oci.Interface = (*testBackend)(nil)

func TestVersion(t *testing.T) {
	handler, err := New(&testBackend{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v2/", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Docker-Distribution-API-Version"); got != "registry/2.0" {
		t.Fatalf("Docker-Distribution-API-Version = %q, want registry/2.0", got)
	}
	if got := response.Body.String(); got != "{}" {
		t.Fatalf("body = %q, want {}", got)
	}
}

func TestCatalogPaginationAcrossNestedRepositories(t *testing.T) {
	backend := &testBackend{Funcs: &oci.Funcs{
		Repositories_: func(_ context.Context, startAfter string) iter.Seq2[string, error] {
			return func(yield func(string, error) bool) {
				for _, repository := range []string{"team/api", "team/web", "tools/builder"} {
					if repository > startAfter && !yield(repository, nil) {
						return
					}
				}
			}
		},
	}}
	handler, err := New(backend)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/v2/_catalog?n=2", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first page status = %d, want %d", first.Code, http.StatusOK)
	}
	var firstPage struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstPage); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	if len(firstPage.Repositories) != 2 || firstPage.Repositories[1] != "team/web" {
		t.Fatalf("first page repositories = %v", firstPage.Repositories)
	}
	if got, want := first.Header().Get("Link"), `</v2/_catalog?last=team%2Fweb&n=2>; rel="next"`; got != want {
		t.Fatalf("Link = %q, want %q", got, want)
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/v2/_catalog?n=2&last=team%2Fweb", nil))
	var secondPage struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondPage); err != nil {
		t.Fatalf("decode second page: %v", err)
	}
	if len(secondPage.Repositories) != 1 || secondPage.Repositories[0] != "tools/builder" {
		t.Fatalf("second page repositories = %v", secondPage.Repositories)
	}
	if got := second.Header().Get("Link"); got != "" {
		t.Fatalf("second page Link = %q, want empty", got)
	}
}

func TestTagsRouteCapturesNestedRepository(t *testing.T) {
	var gotRepository string
	var gotParams *oci.TagsParameters
	backend := &testBackend{Funcs: &oci.Funcs{
		Tags_: func(_ context.Context, repository string, params *oci.TagsParameters) iter.Seq2[string, error] {
			gotRepository = repository
			gotParams = params
			return func(yield func(string, error) bool) {
				if !yield("latest", nil) {
					return
				}
				yield("stable", nil)
			}
		},
	}}
	handler, err := New(backend)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v2/team/api/tags/list?n=1", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", response.Code, http.StatusOK, response.Body)
	}
	if gotRepository != "team/api" {
		t.Fatalf("backend repository = %q, want team/api", gotRepository)
	}
	if gotParams == nil || gotParams.Limit != 2 {
		t.Fatalf("backend tags parameters = %#v, want limit 2", gotParams)
	}
	if got, want := response.Header().Get("Link"), `</v2/team/api/tags/list?last=latest&n=1>; rel="next"`; got != want {
		t.Fatalf("Link = %q, want %q", got, want)
	}
}

func TestTagsUnknownRepository(t *testing.T) {
	handler, err := New(ocimem.New())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v2/team/api/tags/list", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", response.Code, http.StatusNotFound, response.Body)
	}
	var body struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Errors) != 1 || body.Errors[0].Code != "NAME_UNKNOWN" {
		t.Fatalf("errors = %+v, want NAME_UNKNOWN", body.Errors)
	}
}

func TestCatalogRejectsInvalidPageSize(t *testing.T) {
	handler, err := New(&testBackend{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v2/_catalog?n=0", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestCatalogReportsBackendIteratorError(t *testing.T) {
	backend := &testBackend{Funcs: &oci.Funcs{
		Repositories_: func(context.Context, string) iter.Seq2[string, error] {
			return func(yield func(string, error) bool) {
				yield("", errors.New("backend failure"))
			}
		},
	}}
	handler, err := New(backend)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v2/_catalog", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
}

func TestBlobUploadAndRead(t *testing.T) {
	handler, err := New(ocimem.New())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	const repository = "team/api"
	content := []byte("registry blob contents")
	digest := ocidigest.FromBytes(content)

	start := httptest.NewRecorder()
	handler.ServeHTTP(start, httptest.NewRequest(http.MethodPost, "/v2/"+repository+"/blobs/uploads/", nil))
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status = %d, want %d; body: %s", start.Code, http.StatusAccepted, start.Body)
	}
	location := start.Header().Get("Location")
	if location == "" {
		t.Fatal("start response is missing Location")
	}

	split := len(content) / 2
	for _, chunk := range []struct {
		start int
		data  []byte
	}{
		{start: 0, data: content[:split]},
		{start: split, data: content[split:]},
	} {
		patchRequest := httptest.NewRequest(http.MethodPatch, location, bytes.NewReader(chunk.data))
		patchRequest.Header.Set("Content-Type", "application/octet-stream")
		patchRequest.Header.Set("Content-Range", strconv.Itoa(chunk.start)+"-"+strconv.Itoa(chunk.start+len(chunk.data)-1))
		patch := httptest.NewRecorder()
		handler.ServeHTTP(patch, patchRequest)
		if patch.Code != http.StatusAccepted {
			t.Fatalf("patch at offset %d status = %d, want %d; body: %s", chunk.start, patch.Code, http.StatusAccepted, patch.Body)
		}
	}

	finalizeURL := location + "?digest=" + url.QueryEscape(digest.String())
	finalize := httptest.NewRecorder()
	handler.ServeHTTP(finalize, httptest.NewRequest(http.MethodPut, finalizeURL, nil))
	if finalize.Code != http.StatusCreated {
		t.Fatalf("finalize status = %d, want %d; body: %s", finalize.Code, http.StatusCreated, finalize.Body)
	}
	if got := finalize.Header().Get("Docker-Content-Digest"); got != digest.String() {
		t.Fatalf("Docker-Content-Digest = %q, want %q", got, digest)
	}

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v2/"+repository+"/blobs/"+digest.String(), nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d; body: %s", get.Code, http.StatusOK, get.Body)
	}
	if !bytes.Equal(get.Body.Bytes(), content) {
		t.Fatalf("GET body = %q, want %q", get.Body.Bytes(), content)
	}
	if got := get.Header().Get("Content-Length"); got != strconv.Itoa(len(content)) {
		t.Fatalf("Content-Length = %q, want %d", got, len(content))
	}

	rangeRequest := httptest.NewRequest(http.MethodGet, "/v2/"+repository+"/blobs/"+digest.String(), nil)
	rangeRequest.Header.Set("Range", "bytes=2-7")
	rangeResponse := httptest.NewRecorder()
	handler.ServeHTTP(rangeResponse, rangeRequest)
	if rangeResponse.Code != http.StatusPartialContent || rangeResponse.Body.String() != string(content[2:8]) {
		t.Fatalf("range response = %d %q, want %d %q", rangeResponse.Code, rangeResponse.Body.String(), http.StatusPartialContent, content[2:8])
	}

	head := httptest.NewRecorder()
	handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/v2/"+repository+"/blobs/"+digest.String(), nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD status/body = %d/%q, want %d/empty", head.Code, head.Body.String(), http.StatusOK)
	}
}

func TestBlobUploadRejectsDigestMismatch(t *testing.T) {
	handler, err := New(ocimem.New())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	const repository = "team/api"
	content := []byte("uploaded bytes")
	start := httptest.NewRecorder()
	handler.ServeHTTP(start, httptest.NewRequest(http.MethodPost, "/v2/"+repository+"/blobs/uploads/", nil))
	location := start.Header().Get("Location")
	if location == "" {
		t.Fatalf("start response is missing Location: %s", start.Body)
	}

	patchRequest := httptest.NewRequest(http.MethodPatch, location, bytes.NewReader(content))
	patchRequest.Header.Set("Content-Range", "0-"+strconv.Itoa(len(content)-1))
	patch := httptest.NewRecorder()
	handler.ServeHTTP(patch, patchRequest)
	if patch.Code != http.StatusAccepted {
		t.Fatalf("patch status = %d, want %d; body: %s", patch.Code, http.StatusAccepted, patch.Body)
	}

	wrongDigest := ocidigest.FromBytes([]byte("different bytes"))
	finalize := httptest.NewRecorder()
	handler.ServeHTTP(finalize, httptest.NewRequest(http.MethodPut, location+"?digest="+url.QueryEscape(wrongDigest.String()), nil))
	if finalize.Code != http.StatusBadRequest {
		t.Fatalf("finalize status = %d, want %d; body: %s", finalize.Code, http.StatusBadRequest, finalize.Body)
	}
}

func TestGraphQLRouteIsNotServedByRegistry(t *testing.T) {
	handler, err := New(ocimem.New())
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, GraphQLPath, nil))
	if recorder.Body.String() == "graphql" || recorder.Code == http.StatusOK {
		t.Fatalf("status = %d body = %q, want no GraphQL response", recorder.Code, recorder.Body.String())
	}
}
