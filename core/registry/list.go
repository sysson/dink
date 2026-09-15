package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/docker/oci"
	"github.com/docker/oci/ociref"
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
		tags, err := oci.All(client.Tags(ctx, repo, &oci.TagsParameters{
			Limit: -1,
		}))

		if err != nil {
			return nil, err
		}
		for _, tag := range tags {
			desc, err := client.ResolveTag(ctx, repo, tag)
			if err != nil {
				return nil, err
			}
			ref := ociref.Reference{
				Host:       "",
				Repository: repo,
				Tag:        tag,
			}
			summary, ok, err := imageSummary(ctx, client, id.Namespace, ref, desc)
			if err != nil {
				return nil, err
			}
			if ok {
				is = append(is, summary)
			}
		}
	}
	return is, nil
}

// imageSummary describes the image a tag names. An image stored under the tag
// form of its digest is reported as a repository digest instead of a tag,
// which is how it was pulled. The summary is dropped when the image's content
// is not held locally, as is the case for the platforms of an index that were
// never pulled.
func imageSummary(ctx context.Context, client oci.Interface, namespace string, ref ociref.Reference, desc oci.Descriptor) (imagetypes.Summary, bool, error) {

	var totalSize int64
	totalSize = desc.Size
	manifest, err := client.GetManifest(ctx, ref.Repository, desc.Digest)
	if err != nil {
		return imagetypes.Summary{}, false, err
	}
	m, err := readBlob[*oci.IndexOrManifest](manifest)
	if err != nil {
		return imagetypes.Summary{}, false, err
	}

	// An index carries no config or layers of its own, so the image it
	// resolves to for this platform is what the summary describes.

	if isIndex(desc.MediaType) {
		selected, err := selectManifests(nil, m)
		if err != nil {
			return imagetypes.Summary{}, false, nil
		}
		if len(selected) == 0 {
			return imagetypes.Summary{}, false, nil
		}
		child, err := client.GetManifest(ctx, ref.Repository, selected[0].Digest)
		if isNotFound(err) {

			return imagetypes.Summary{}, false, nil
		}
		if err != nil {
			return imagetypes.Summary{}, false, err
		}
		if m, err = readBlob[*oci.IndexOrManifest](child); err != nil {
			return imagetypes.Summary{}, false, err
		}
		totalSize += selected[0].Size
	}
	if m.Config == nil {
		return imagetypes.Summary{}, false, nil
	}
	totalSize += m.Config.Size
	for _, layer := range m.Layers {
		totalSize += layer.Size
	}

	img, err := image(ctx, client, ref.Repository, *m.Config)
	if err != nil {
		return imagetypes.Summary{}, false, err
	}
	var created int64
	if img.Created != nil {
		created = img.Created.Unix()
	}

	ref.Repository = strings.TrimPrefix(ref.Repository, namespace+"/")
	// The name an image pulled by digest is displayed under is the digest
	// reference itself, since it has no tag.
	name := fmt.Sprintf("%s:%s", ref.Repository, ref.Tag)
	digestRef := fmt.Sprintf("%s@%s", ref.Repository, desc.Digest)
	if _, byDigest := digestForTag(ref.Tag); byDigest {
		name = digestRef
	}

	return imagetypes.Summary{
		RepoTags:    []string{name},
		RepoDigests: []string{digestRef},
		Created:     created,
		// With a containerd-style store the image ID is the digest of what the
		// reference resolves to, not of the config blob.
		ID: desc.Digest.String(),
		Descriptor: &ocispec.Descriptor{
			MediaType: desc.MediaType,
			Digest:    ocidigest.Digest(desc.Digest.String()),
			Size:      totalSize,
			Platform:  &img.Platform,
		},
		Size: totalSize,
	}, true, nil
}

// image reads and decodes the config blob of an image, returning the resulting ocispec.Image structure.
func image(ctx context.Context, client oci.Interface, repo string, config oci.Descriptor) (*ocispec.Image, error) {
	blob, err := client.GetBlob(ctx, repo, config.Digest)
	if err != nil {
		return nil, fmt.Errorf("get config %s: %w", config.Digest, err)
	}
	defer func() { _ = blob.Close() }()

	var image ocispec.Image
	if err := json.NewDecoder(blob).Decode(&image); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", config.Digest, err)
	}
	return &image, nil
}
