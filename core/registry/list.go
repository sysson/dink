package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/oci"
	imagetypes "github.com/moby/moby/api/types/image"
	ocidigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/types"
)

func (r *RegistryService) Images(ctx context.Context, options types.ImageListOptions) ([]imagetypes.Summary, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("missing identity in context")
	}
	client, err := r.client()
	if err != nil {
		return nil, err
	}
	is := make([]imagetypes.Summary, 0)
	repos, err := oci.All(client.Repositories(ctx, id.Namespace))
	if err != nil {
		return nil, err
	}
	for _, repo := range repos {
		if id.Namespace != "" && !strings.HasPrefix(repo, id.Namespace) {
			continue
		}
		tags, err := oci.All(client.Tags(ctx, repo, nil))
		if err != nil {
			return nil, err
		}
		for _, tag := range tags {
			desc, err := client.ResolveTag(ctx, repo, tag)
			if err != nil {
				return nil, err
			}
			manifest, err := client.GetManifest(ctx, repo, desc.Digest)
			if err != nil {
				return nil, err
			}
			fm, err := readManifest(manifest)
			if err != nil {
				return nil, err
			}
			var totalSize int64
			for _, layer := range fm.layers {
				totalSize += layer.Size
			}
			is = append(is, imagetypes.Summary{
				RepoTags: []string{fmt.Sprintf("%s:%s", repo, tag)},
				ID:       string(fm.config.Digest),
				Descriptor: &ocispec.Descriptor{
					MediaType: desc.MediaType,
					Digest:    ocidigest.Digest(desc.Digest.String()),
					Size:      totalSize,
				},
				Size: totalSize,
			})
		}
	}
	return is, nil
}
