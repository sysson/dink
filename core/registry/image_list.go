package registry

import (
	"context"

	"github.com/docker/oci"
	imagetypes "github.com/moby/moby/api/types/image"
)

func (r *RegistryService) listManifestSummaries(ctx context.Context, repository string, tree *storedImageTree) ([]imagetypes.ManifestSummary, int64, error) {
	client, err := r.client()
	if err != nil {
		return nil, 0, err
	}
	children := tree.Manifests
	if !isIndex(tree.MediaType) {
		var platform *storedPlatform
		if config := tree.ImageConfig; config != nil {
			platform = &storedPlatform{OS: config.OS, Architecture: config.Architecture, Variant: optional(config.Variant)}
		}
		children = []storedIndexChild{{
			Digest: tree.Digest, MediaType: tree.MediaType, Size: tree.Size,
			Platform: platform, Manifest: &tree.storedImage,
		}}
	}
	summaries := manifestSummaries(&storedImageTree{Manifests: children})
	// Count shared content once in the parent, but in each platform that uses it.
	present := map[string]int64{tree.Digest: tree.Size}
	missing := map[string]bool{}
	for index, child := range children {
		summary := &summaries[index]
		summary.Available = false
		summary.Size.Content = 0
		summary.Size.Total = 0
		if child.Manifest == nil {
			continue
		}
		present[child.Digest] = child.Size
		summary.Size.Content = child.Size
		available := child.Manifest.Config != nil
		blobs := make([]storedSized, 0, len(child.Manifest.Layers)+1)
		if child.Manifest.Config != nil {
			blobs = append(blobs, *child.Manifest.Config)
		}
		for _, layer := range child.Manifest.Layers {
			blobs = append(blobs, storedSized{Digest: layer.Digest, Size: layer.Size})
		}
		counted := map[string]bool{child.Digest: true}
		for _, blob := range blobs {
			if counted[blob.Digest] {
				continue
			}
			counted[blob.Digest] = true
			size, found := present[blob.Digest]
			if !found && !missing[blob.Digest] {
				descriptor, err := client.ResolveBlob(ctx, repository, oci.Digest(blob.Digest))
				if isNotFound(err) {
					missing[blob.Digest] = true
				} else if err != nil {
					return nil, 0, err
				} else {
					size, found = descriptor.Size, true
					present[blob.Digest] = size
				}
			}
			if found {
				summary.Size.Content += size
			} else {
				available = false
			}
		}
		summary.Available = available
		summary.Size.Total = summary.Size.Content
	}
	var total int64
	for _, size := range present {
		total += size
	}
	return summaries, total, nil
}
