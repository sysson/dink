package registry

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/docker/oci/ociref"
	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/syskit/httpx"
	"github.com/sysson/syskit/logx"
)

func (r *RegistryService) ImageDelete(ctx context.Context, name string, options imagebackend.RemoveOptions) ([]imagetypes.DeleteResponse, error) {
	records := []imagetypes.DeleteResponse{}

	if len(options.Platforms) > 1 {
		return nil, httpx.BadRequest(errors.New("selecting multiple platforms is not supported"))
	}

	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("missing identity in context")
	}

	client, err := r.client()
	if err != nil {
		return nil, err
	}

	ref, err := ociref.ParseRelative(name)
	if err != nil {
		return nil, err
	}

	ref = repositoryFor(id, ref)

	tagDesc, err := checkTagDigest(ctx, client, ref)
	if err != nil {
		return nil, err
	}
	ref.Digest = tagDesc.Digest

	m := manifest{
		platform:   platformMatcher(options.Platforms),
		client:     client,
		ref:        ref,
		descriptor: tagDesc,
	}

	descriptors, err := m.AllDescriptors(ctx)
	if err != nil {
		return nil, err
	}

	for _, desc := range slices.Backward(descriptors) {
		if isIndex(desc.MediaType) || isManifest(desc.MediaType) {
			err := client.DeleteManifest(ctx, ref.Repository, desc.Digest)
			if err != nil {
				logx.G(ctx).Error("failed to delete manifest %s: %v", desc.Digest.String(), err)
				continue
			}
		} else {
			err = client.DeleteBlob(ctx, ref.Repository, desc.Digest)
			if err != nil {
				logx.G(ctx).Error("failed to delete blob %s: %v", desc.Digest.String(), err)
				continue
			}
		}
		records = append(records, imagetypes.DeleteResponse{
			Deleted: desc.Digest.String(),
		})
	}

	records = append(records, imagetypes.DeleteResponse{
		Deleted: tagDesc.Digest.String(),
	})

	return records, nil
}
