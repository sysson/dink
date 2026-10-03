package image

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/oci/ociref"
	imagetypes "github.com/moby/moby/api/types/image"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/types"
	"github.com/sysson/dink/pkg/filters"
)

type listTranslatorStub struct {
	Translator
	options types.ImageListOptions
}

func (s *listTranslatorStub) Images(_ context.Context, options types.ImageListOptions) ([]imagetypes.Summary, error) {
	s.options = options
	image := imagetypes.Summary{ID: "sha256:index", RepoTags: []string{"app:latest"}}
	if options.Manifests {
		image.Manifests = []imagetypes.ManifestSummary{{
			ID: "sha256:amd64", Available: true, Kind: imagetypes.ManifestKindImage,
			ImageData: &imagetypes.ImageProperties{Platform: ocispec.Platform{OS: "linux", Architecture: "amd64"}},
		}, {
			ID: "sha256:arm64", Available: false, Kind: imagetypes.ManifestKindImage,
			ImageData: &imagetypes.ImageProperties{Platform: ocispec.Platform{OS: "linux", Architecture: "arm64"}},
		}}
	}
	return []imagetypes.Summary{image}, nil
}

func TestGetImagesJSONManifests(t *testing.T) {
	for _, tc := range []struct {
		version   string
		query     string
		manifests bool
	}{
		{version: "1.46", query: "?manifests=true"},
		{version: "1.47", query: "?manifests=true", manifests: true},
		{version: "1.55", query: "?manifests=false"},
		{version: "1.55"},
	} {
		t.Run(tc.version+tc.query, func(t *testing.T) {
			stub := &listTranslatorStub{}
			api := &imageRouter{translator: stub}
			request := httptest.NewRequest(http.MethodGet, "/images/json"+tc.query, nil)
			request.SetPathValue("version", tc.version)
			response := httptest.NewRecorder()
			if err := api.getImagesJSON(response, request); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || stub.options.Manifests != tc.manifests {
				t.Fatalf("status/options = %d/%+v", response.Code, stub.options)
			}
			var images []imagetypes.Summary
			if err := json.Unmarshal(response.Body.Bytes(), &images); err != nil {
				t.Fatal(err)
			}
			if len(images) != 1 {
				t.Fatalf("images = %+v", images)
			}
			if tc.manifests {
				if len(images[0].Manifests) != 2 || !images[0].Manifests[0].Available || images[0].Manifests[1].Available {
					t.Fatalf("manifest availability = %+v", images[0].Manifests)
				}
			} else if strings.Contains(response.Body.String(), `"Manifests"`) {
				t.Fatalf("ordinary list includes manifests: %s", response.Body.String())
			}
		})
	}
}

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

type pruneTranslatorStub struct {
	Translator
	filters filters.Args
	report  *imagetypes.PruneReport
}

func (s *pruneTranslatorStub) ImagePrune(_ context.Context, pruneFilters filters.Args) (*imagetypes.PruneReport, error) {
	s.filters = pruneFilters
	return s.report, nil
}

func TestPostImagesPrune(t *testing.T) {
	translator := &pruneTranslatorStub{report: &imagetypes.PruneReport{
		ImagesDeleted:  []imagetypes.DeleteResponse{{Untagged: "unused:latest"}},
		SpaceReclaimed: 42,
	}}
	api := &imageRouter{translator: translator}
	request := httptest.NewRequest(http.MethodPost, "/images/prune", strings.NewReader(`filters={"dangling":{"false":true}}`))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	if err := api.postImagesPrune(response, request); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if !translator.filters.ExactMatch("dangling", "false") {
		t.Fatalf("prune filters = %+v, want dangling=false", translator.filters)
	}
	var report imagetypes.PruneReport
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.SpaceReclaimed != 42 || len(report.ImagesDeleted) != 1 || report.ImagesDeleted[0].Untagged != "unused:latest" {
		t.Fatalf("prune report = %+v", report)
	}
}
