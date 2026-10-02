package distribution

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/moby/moby/api/pkg/authconfig"
	registrytypes "github.com/moby/moby/api/types/registry"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type distributionStub struct {
	name string
	auth *registrytypes.AuthConfig
	err  error
}

func (s *distributionStub) GetDistributionInfo(_ context.Context, name string, auth *registrytypes.AuthConfig) (registrytypes.DistributionInspect, error) {
	s.name, s.auth = name, auth
	return registrytypes.DistributionInspect{Descriptor: ocispec.Descriptor{
		Digest: digest.FromString("manifest"), MediaType: ocispec.MediaTypeImageManifest, Size: 123,
	}, Platforms: []ocispec.Platform{{OS: "linux", Architecture: "amd64"}}}, s.err
}

func TestDistributionInfoRoute(t *testing.T) {
	auth, err := authconfig.Encode(registrytypes.AuthConfig{Username: "user", Password: "password"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/distribution/example.com/app:test/json", nil)
	request.SetPathValue("name", "example.com/app:test")
	request.Header.Set(registrytypes.AuthHeader, auth)
	stub := &distributionStub{}
	response := httptest.NewRecorder()
	if err := (&distributionRouter{translator: stub}).getDistributionInfo(response, request); err != nil {
		t.Fatal(err)
	}
	var result registrytypes.DistributionInspect
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || result.Descriptor.Digest != digest.FromString("manifest") ||
		len(result.Platforms) != 1 || result.Platforms[0].Architecture != "amd64" ||
		stub.name != "example.com/app:test" || stub.auth.Username != "user" || stub.auth.Password != "password" {
		t.Fatalf("incorrect distribution response/call: %v %+v", result, stub)
	}
	request.Header.Set(registrytypes.AuthHeader, "invalid")
	if err := (&distributionRouter{translator: stub}).getDistributionInfo(httptest.NewRecorder(), request); err == nil {
		t.Fatal("malformed registry auth was accepted")
	}
	request.Header.Set(registrytypes.AuthHeader, auth)
	stub.err = errors.New("upstream lookup failed")
	if err := (&distributionRouter{translator: stub}).getDistributionInfo(httptest.NewRecorder(), request); !errors.Is(err, stub.err) {
		t.Fatalf("upstream error was hidden: %v", err)
	}
}
