package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/docker/oci"
	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	ocidigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/types"
	"github.com/sysson/ocistore"
	"github.com/sysson/ocistore/backend"
	"github.com/sysson/syskit/httpx"
)

const indexPage = 1000

type storedPlatform struct {
	OS           string
	Architecture string
	Variant      *string
	OSVersion    *string
	OSFeatures   []string
}

type storedSized struct {
	Digest string
	Size   int64
}

type storedDescriptor struct {
	Digest      string
	Size        int64
	MediaType   string
	Annotations []storedAnnotation
}

type storedImageConfig struct {
	Created      *time.Time
	OS           string
	Architecture string
	Variant      string
	// Raw is the verbatim config JSON.
	Raw string
}

type storedImage struct {
	Digest      string
	MediaType   string
	Size        int64
	Config      *storedSized
	Layers      []storedDescriptor
	ImageConfig *storedImageConfig
}

// storedImageTree is a manifest together with the index children and tags
// that describe the image a reference resolves to.
type storedImageTree struct {
	storedImage
	Tags      []string
	Manifests []storedIndexChild
}

type storedIndexChild struct {
	Digest      string
	MediaType   string
	Size        int64
	Platform    *storedPlatform
	Annotations []storedAnnotation
	// Manifest is nil when the child was not pulled.
	Manifest *storedImage
}

type storedAnnotation struct {
	Key   string
	Value string
}

// manifest reads the manifest digest names in repository, reporting whether
// it is stored.
func (r *RegistryService) manifest(ctx context.Context, repository string, digest oci.Digest) (backend.ManifestRecord, bool, error) {
	record, err := r.index.Manifest(ctx, repository, digest)
	if isNotFound(err) || errors.Is(err, oci.ErrNameInvalid) {
		return backend.ManifestRecord{}, false, nil
	}
	return record, err == nil, err
}

// imageTree loads the manifest digest names in repository and, for an index,
// its children. It returns nil when the manifest is not stored.
func (r *RegistryService) imageTree(ctx context.Context, repository string, digest oci.Digest) (*storedImageTree, error) {
	record, found, err := r.manifest(ctx, repository, digest)
	if err != nil || !found {
		return nil, err
	}
	image, err := r.storedImage(ctx, record)
	if err != nil {
		return nil, err
	}
	tree := &storedImageTree{storedImage: *image, Tags: record.Tags}
	for _, child := range record.Manifests {
		entry := storedIndexChild{
			Digest:      string(child.Digest),
			MediaType:   child.MediaType,
			Size:        child.Size,
			Platform:    platformOf(child.Platform),
			Annotations: annotationList(child.Annotations),
		}
		childRecord, found, err := r.manifest(ctx, repository, child.Digest)
		if err != nil {
			return nil, err
		}
		if found {
			if entry.Manifest, err = r.storedImage(ctx, childRecord); err != nil {
				return nil, err
			}
		}
		tree.Manifests = append(tree.Manifests, entry)
	}
	return tree, nil
}

func (r *RegistryService) storedImage(ctx context.Context, record backend.ManifestRecord) (*storedImage, error) {
	image := &storedImage{
		Digest:    string(record.Descriptor.Digest),
		MediaType: record.Descriptor.MediaType,
		Size:      record.Descriptor.Size,
	}
	for _, layer := range record.Layers {
		image.Layers = append(image.Layers, storedDescriptor{
			Digest:      string(layer.Digest),
			Size:        layer.Size,
			MediaType:   layer.MediaType,
			Annotations: annotationList(layer.Annotations),
		})
	}
	if record.Config == nil {
		return image, nil
	}
	image.Config = &storedSized{Digest: string(record.Config.Digest), Size: record.Config.Size}
	raw, err := r.index.ImageConfig(ctx, *record.Config)
	if err != nil || raw == nil {
		return image, err
	}
	var config struct {
		Created      *time.Time `json:"created"`
		OS           string     `json:"os"`
		Architecture string     `json:"architecture"`
		Variant      string     `json:"variant"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("decoding image config %s: %w", record.Config.Digest, err)
	}
	if config.Created != nil && config.Created.IsZero() {
		config.Created = nil
	}
	image.ImageConfig = &storedImageConfig{
		Created:      config.Created,
		OS:           config.OS,
		Architecture: config.Architecture,
		Variant:      config.Variant,
		Raw:          string(raw),
	}
	return image, nil
}

func platformOf(platform *oci.Platform) *storedPlatform {
	if platform == nil {
		return nil
	}
	return &storedPlatform{
		OS:           platform.OS,
		Architecture: platform.Architecture,
		Variant:      optional(platform.Variant),
		OSVersion:    optional(platform.OSVersion),
		OSFeatures:   platform.OSFeatures,
	}
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// annotationList sorts annotations by key.
func annotationList(annotations map[string]string) []storedAnnotation {
	result := make([]storedAnnotation, 0, len(annotations))
	for key, value := range annotations {
		result = append(result, storedAnnotation{Key: key, Value: value})
	}
	slices.SortFunc(result, func(a, b storedAnnotation) int { return strings.Compare(a.Key, b.Key) })
	return result
}

// Images lists the tagged images in the caller's namespace from the metadata
// index. Index images are summarised by the manifest for the default
// platform; tags whose image content was never pulled are omitted.
func (r *RegistryService) Images(ctx context.Context, options types.ImageListOptions) ([]imagetypes.Summary, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("missing identity in context")
	}
	repos, err := r.namespaceRepositories(ctx, id.Namespace)
	if err != nil {
		return nil, err
	}
	summaries := make([]imagetypes.Summary, 0)
	for _, repo := range repos {
		after := ""
		for {
			tags, err := r.index.TagRecords(ctx, repo, after, indexPage)
			if isNotFound(err) {
				break
			}
			if err != nil {
				return nil, err
			}
			for _, tag := range tags {
				tree, err := r.imageTree(ctx, repo, tag.Digest)
				if err != nil {
					return nil, err
				}
				if summary, ok := imageSummary(id.Namespace, repo, tag.Tag, tree); ok {
					if options.Manifests {
						manifests, size, err := r.listManifestSummaries(ctx, repo, tree)
						if err != nil {
							return nil, err
						}
						summary.Manifests = manifests
						summary.Size = size
						summary.Descriptor.Size = tree.Size
						if isIndex(tree.MediaType) {
							summary.Descriptor.Platform = nil
						}
					}
					summaries = append(summaries, summary)
				}
			}
			if len(tags) < indexPage {
				break
			}
			after = tags[len(tags)-1].Tag
		}
	}
	return summaries, nil
}

func (r *RegistryService) namespaceRepositories(ctx context.Context, namespace string) ([]string, error) {
	prefix, after := "", ""
	if namespace != "" {
		prefix = namespace + "/"
		// Repositories sort by name, so start just before the namespace.
		after = namespace + "."
	}
	var result []string
	for {
		names, err := r.index.Repositories(ctx, after, indexPage)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			if !strings.HasPrefix(name, prefix) {
				if name > prefix {
					return result, nil
				}
				continue
			}
			result = append(result, name)
		}
		if len(names) < indexPage {
			return result, nil
		}
		after = names[len(names)-1]
	}
}

// imageSummary describes the image a tag names. An image stored under the tag
// form of its digest is reported as a repository digest instead of a tag,
// which is how it was pulled.
func imageSummary(namespace, repo, tag string, root *storedImageTree) (imagetypes.Summary, bool) {
	if root == nil {
		return imagetypes.Summary{}, false
	}
	image := &root.storedImage
	totalSize := root.Size
	if isIndex(root.MediaType) {
		child, ok := presentPlatformChild(root.MediaType, root.Manifests)
		if !ok {
			return imagetypes.Summary{}, false
		}
		image = child.Manifest
		totalSize += image.Size
	}
	if image.Config == nil {
		return imagetypes.Summary{}, false
	}
	totalSize += image.Config.Size
	for _, layer := range image.Layers {
		totalSize += layer.Size
	}
	var created int64
	platform := ocispec.Platform{}
	if config := image.ImageConfig; config != nil {
		if config.Created != nil {
			created = config.Created.Unix()
		}
		platform.OS = config.OS
		platform.Architecture = config.Architecture
		platform.Variant = config.Variant
	}

	display := strings.TrimPrefix(repo, namespace+"/")
	name := display + ":" + tag
	digestRef := display + "@" + root.Digest
	if _, byDigest := digestForTag(tag); byDigest {
		name = digestRef
	}
	return imagetypes.Summary{
		RepoTags:    []string{name},
		RepoDigests: []string{digestRef},
		Created:     created,
		// With a containerd-style store the image ID is the digest of what the
		// reference resolves to, not of the config blob.
		ID: root.Digest,
		Descriptor: &ocispec.Descriptor{
			MediaType: root.MediaType,
			Digest:    ocidigest.Digest(root.Digest),
			Size:      totalSize,
			Platform:  &platform,
		},
		Size: totalSize,
	}, true
}

// presentPlatformChild picks the index child that describes the image. Only
// children that were pulled are stored, so it prefers the default platform
// among those and otherwise falls back to the first stored platform manifest.
func presentPlatformChild(mediaType string, children []storedIndexChild) (storedIndexChild, bool) {
	var present []storedIndexChild
	for _, child := range children {
		if child.Manifest != nil && child.Platform != nil && child.Platform.OS != "unknown" {
			present = append(present, child)
		}
	}
	if child, ok := defaultPlatformChild(mediaType, present); ok {
		return child, true
	}
	if len(present) > 0 {
		return present[0], true
	}
	return storedIndexChild{}, false
}

func defaultPlatformChild(mediaType string, children []storedIndexChild) (storedIndexChild, bool) {
	index := &oci.IndexOrManifest{MediaType: mediaType}
	for _, child := range children {
		descriptor := oci.Descriptor{Digest: oci.Digest(child.Digest), MediaType: child.MediaType, Size: child.Size}
		if child.Platform != nil {
			descriptor.Platform = &oci.Platform{
				OS:           child.Platform.OS,
				Architecture: child.Platform.Architecture,
				Variant:      deref(child.Platform.Variant),
				OSVersion:    deref(child.Platform.OSVersion),
				OSFeatures:   child.Platform.OSFeatures,
			}
		}
		index.Manifests = append(index.Manifests, descriptor)
	}
	selected, err := parseIndexManifest(nil, index)
	if err != nil {
		return storedIndexChild{}, false
	}
	for _, child := range children {
		if oci.Digest(child.Digest) == selected.platformDigest {
			return child, true
		}
	}
	return storedIndexChild{}, false
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// ImageDelete untags name and, when no other tag or manifest in the
// repository still uses the image, removes the image's manifests and blobs
// from the repository. name may be a tag, a digest reference or a full or
// truncated image ID. Removal only updates metadata; registry garbage
// collection deletes content once nothing references it anywhere. Deleted
// records list the digests whose content no longer has any user.
func (r *RegistryService) ImageDelete(ctx context.Context, name string, options imagebackend.RemoveOptions) ([]imagetypes.DeleteResponse, error) {
	if len(options.Platforms) > 0 {
		return nil, httpx.BadRequest(errors.New("removing individual platforms is not supported"))
	}
	local, err := r.client()
	if err != nil {
		return nil, err
	}
	resolved, err := r.resolveImage(ctx, name)
	if err != nil {
		return nil, err
	}

	record, found, err := r.manifest(ctx, resolved.repository, resolved.digest)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, httpx.NotFound(fmt.Errorf("no such image: %s", name))
	}
	closure, err := r.index.Closure(ctx, resolved.repository, resolved.digest)
	if isNotFound(err) {
		return nil, httpx.NotFound(fmt.Errorf("no such image: %s", name))
	}
	if err != nil {
		return nil, err
	}

	// A tagged name removes just that tag. An ID or digest names the image
	// itself, which docker only removes unforced while a single tag holds it.
	remove := []string{resolved.tag}
	if resolved.tag == "" {
		remove = record.Tags
		if len(remove) > 1 && !options.Force {
			return nil, httpx.Conflict(fmt.Errorf("unable to delete %s (must be forced) - image is referenced in multiple tags", name))
		}
	}

	records := make([]imagetypes.DeleteResponse, 0, len(remove))
	for _, tag := range remove {
		untagged := resolved.display() + ":" + tag
		if _, byDigest := digestForTag(tag); byDigest {
			untagged = resolved.display() + "@" + record.Descriptor.Digest.String()
		}
		if err := local.DeleteTag(ctx, resolved.repository, tag); err != nil {
			return records, fmt.Errorf("untagging %s: %w", untagged, err)
		}
		records = append(records, imagetypes.DeleteResponse{Untagged: untagged})
	}

	remaining := 0
	for _, tag := range record.Tags {
		if !slices.Contains(remove, tag) {
			remaining++
		}
	}
	if remaining > 0 || len(closure) == 0 || closure[0].RetainedInRepository {
		return records, nil
	}

	// Manifests go first, parents before children, so every blob is
	// unreferenced in the repository by the time it is removed.
	var deleted []ocistore.ClosureEntry
	for _, pass := range []bool{true, false} {
		for _, entry := range closure {
			if (entry.Role == ocistore.RoleManifest) != pass || entry.RetainedInRepository {
				continue
			}
			digest := entry.Descriptor.Digest
			if pass {
				err = local.DeleteManifest(ctx, resolved.repository, digest)
			} else {
				err = local.DeleteBlob(ctx, resolved.repository, digest)
			}
			if err != nil && !isNotFound(err) {
				return records, fmt.Errorf("removing %s from %s: %w", digest, resolved.repository, err)
			}
			deleted = append(deleted, entry)
		}
	}
	for _, entry := range deleted {
		if !entry.Shared {
			records = append(records, imagetypes.DeleteResponse{Deleted: entry.Descriptor.Digest.String()})
		}
	}
	return records, nil
}
