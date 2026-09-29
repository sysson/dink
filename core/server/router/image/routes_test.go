package image

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/docker/oci/ociref"
)

type tagTranslatorStub struct {
	Translator
	name string
	ref  ociref.Reference
}

func (s *tagTranslatorStub) TagImage(_ context.Context, name string, ref ociref.Reference) error {
	s.name, s.ref = name, ref
	return nil
}

func TestPostImagesTag(t *testing.T) {
	stub := &tagTranslatorStub{}
	api := &imageRouter{translator: stub}
	request := httptest.NewRequest(http.MethodPost, "/images/source:v1/tag?repo=example/app&tag=release", nil)
	request.SetPathValue("name", "source:v1")
	response := httptest.NewRecorder()
	if err := api.postImagesTag(response, request); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusCreated || response.Body.Len() != 0 {
		t.Fatalf("tag response = %d %q, want 201 with no body", response.Code, response.Body.String())
	}
	if stub.name != "source:v1" || stub.ref.Repository != "example/app" || stub.ref.Tag != "release" {
		t.Fatalf("TagImage called with %q, %+v", stub.name, stub.ref)
	}
}
