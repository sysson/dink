package translator

import (
	"context"
	"errors"

	"github.com/docker/oci/ociref"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/sysson/dink/core/identity"
	dinktypes "github.com/sysson/dink/core/types"
	"github.com/sysson/syskit/httpx"
)

type fakeRegistry struct {
	digests         map[string]string
	configs         map[string]*dockerspec.DockerOCIImageConfig
	images          []imagetypes.Summary
	issued          int
	credentialError error
	pullImageName   string
	pullCalls       int
	pullAuth        *registry.AuthConfig
	pullRef         ociref.Reference
	pullErr         error
}

func (f *fakeRegistry) Authenticate(context.Context, *registry.AuthConfig) (string, error) {
	return "", nil
}

func (f *fakeRegistry) ImageInspect(_ context.Context, name string, _ imagebackend.ImageInspectOpts) (*imagebackend.InspectData, error) {
	digest, ok := f.digests[name]
	if !ok {
		return nil, httpx.NotFound(errors.New("no such image: " + name))
	}
	result := &imagebackend.InspectData{ID: digest, RepoDigests: []string{name + "@" + digest}}
	result.Config = f.configs[name]
	return result, nil
}

func (f *fakeRegistry) PullImage(_ context.Context, ref ociref.Reference, options imagebackend.PullOptions) error {
	f.pullCalls++
	f.pullAuth = options.AuthConfig
	f.pullRef = ref
	if f.pullErr != nil {
		return f.pullErr
	}
	if f.pullImageName != "" {
		f.digests[f.pullImageName] = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	}
	return nil
}

func (f *fakeRegistry) Images(context.Context, dinktypes.ImageListOptions) ([]imagetypes.Summary, error) {
	return f.images, nil
}

func (f *fakeRegistry) EnsurePullCredential(ctx context.Context, existingPassword string) (string, string, error) {
	if f.credentialError != nil {
		return "", "", f.credentialError
	}
	if existingPassword != "secret" {
		f.issued++
	}
	id, _ := identity.FromContext(ctx)
	return id.Namespace, "secret", nil
}
