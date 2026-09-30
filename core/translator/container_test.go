package translator

import (
	"context"
	"errors"

	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/sysson/dink/core/identity"
	dinktypes "github.com/sysson/dink/core/types"
)

type fakeRegistry struct {
	digests map[string]string
	configs map[string]*dockerspec.DockerOCIImageConfig
	images  []imagetypes.Summary
	issued  int
}

func (f *fakeRegistry) Authenticate(context.Context, *registry.AuthConfig) (string, error) {
	return "", nil
}

func (f *fakeRegistry) ImageInspect(_ context.Context, name string, _ imagebackend.ImageInspectOpts) (*imagebackend.InspectData, error) {
	digest, ok := f.digests[name]
	if !ok {
		return nil, errors.New("no such image: " + name)
	}
	result := &imagebackend.InspectData{ID: digest, RepoDigests: []string{name + "@" + digest}}
	result.Config = f.configs[name]
	return result, nil
}

func (f *fakeRegistry) Images(context.Context, dinktypes.ImageListOptions) ([]imagetypes.Summary, error) {
	return f.images, nil
}

func (f *fakeRegistry) IssuePullCredential(ctx context.Context) (string, string, error) {
	f.issued++
	id, _ := identity.FromContext(ctx)
	return id.Namespace, "secret", nil
}
