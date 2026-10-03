package image

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/docker/oci/ociref"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/syskit/httpx"
)

func (ir *imageRouter) getImagesGet(w http.ResponseWriter, r *http.Request) error {
	return nil
}

// imageName is the image a request names, which may be a repository with an
// optional tag, a digest reference or a full or truncated image ID.
func imageName(r *http.Request) (string, error) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		return "", httpx.BadRequest(errors.New("name parameter is required"))
	}
	return name, nil
}

// decodePlatform reads the JSON-encoded OCI platform docker clients send.
func decodePlatform(value string) (*ocispec.Platform, error) {
	if value == "" {
		return nil, nil
	}
	var platform ocispec.Platform
	if err := json.Unmarshal([]byte(value), &platform); err != nil {
		return nil, httpx.BadRequest(fmt.Errorf("failed to parse platform: %w", err))
	}
	if platform.OS == "" || platform.Architecture == "" {
		return nil, httpx.BadRequest(errors.New("both OS and Architecture must be provided"))
	}
	return &platform, nil
}

func (ir *imageRouter) postImagesLoad(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) postImagesTag(w http.ResponseWriter, r *http.Request) error {
	name, err := imageName(r)
	if err != nil {
		return err
	}
	repository := strings.TrimSpace(r.URL.Query().Get("repo"))
	if repository == "" {
		return httpx.BadRequest(errors.New("repo parameter is required"))
	}
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))
	if tag == "" {
		tag = "latest"
	}
	ref, err := ociref.ParseRelative(repository + ":" + tag)
	if err != nil {
		return httpx.BadRequest(err)
	}
	if err := ir.translator.TagImage(r.Context(), name, ref); err != nil {
		return err
	}
	w.WriteHeader(http.StatusCreated)
	return nil
}

func (ir *imageRouter) postImagesPrune(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return httpx.BadRequest(err)
	}
	pruneFilters, err := filters.FromJSON(r.Form.Get("filters"))
	if err != nil {
		return httpx.BadRequest(err)
	}
	report, err := ir.translator.ImagePrune(r.Context(), pruneFilters)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, report)
}
