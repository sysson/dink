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
	"github.com/sysson/syskit/httpx"
)

const queryPage = 1000

// Query executes a GraphQL document against the registry metadata and
// decodes its data into out.
func (r *RegistryService) Query(ctx context.Context, document string, variables map[string]any, out any) error {
	if r.queries == nil {
		return errors.New("registry metadata queries are not available")
	}
	response := r.queries.Exec(ctx, document, "", variables)
	if len(response.Errors) > 0 {
		return fmt.Errorf("metadata query: %w", response.Errors[0])
	}
	return json.Unmarshal(response.Data, out)
}

type queryPlatform struct {
	OS           string   `json:"os"`
	Architecture string   `json:"architecture"`
	Variant      *string  `json:"variant"`
	OSVersion    *string  `json:"osVersion"`
	OSFeatures   []string `json:"osFeatures"`
}

type querySized struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// queryDescriptor is querySized with the fields only inspect and
// attestations ask for; they stay empty for queries that omit them.
type queryDescriptor struct {
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	MediaType   string            `json:"mediaType"`
	Annotations []queryAnnotation `json:"annotations"`
}

type queryImageConfig struct {
	Created      *time.Time `json:"created"`
	OS           *string    `json:"os"`
	Architecture *string    `json:"architecture"`
	Variant      *string    `json:"variant"`
	// Raw is the verbatim config JSON, requested only by inspect.
	Raw string `json:"raw"`
}

type queryImage struct {
	Digest      string            `json:"digest"`
	MediaType   string            `json:"mediaType"`
	Size        int64             `json:"size"`
	Config      *querySized       `json:"config"`
	Layers      []queryDescriptor `json:"layers"`
	ImageConfig *queryImageConfig `json:"imageConfig"`
}

// queryImageTree is a manifest together with the index children and tags
// that describe the image a reference resolves to.
type queryImageTree struct {
	queryImage
	Tags      []string          `json:"tags"`
	Manifests []queryIndexChild `json:"manifests"`
}

type queryIndexChild struct {
	Digest      string            `json:"digest"`
	MediaType   string            `json:"mediaType"`
	Size        int64             `json:"size"`
	Platform    *queryPlatform    `json:"platform"`
	Annotations []queryAnnotation `json:"annotations"`
	Manifest    *queryImage       `json:"manifest"`
}

type queryAnnotation struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type queryTag struct {
	Name     string          `json:"name"`
	Manifest *queryImageTree `json:"manifest"`
}

const imageFields = `digest mediaType size config { digest size } layers { digest size }
	imageConfig { created os architecture variant }`

// inspectFields adds what inspect, history and attestations read on top of
// [imageFields]: the raw config JSON and the layer media types and
// annotations that identify in-toto statements.
const inspectFields = `digest mediaType size config { digest size }
	layers { digest size mediaType annotations { key value } }
	imageConfig { created os architecture variant raw }`

// imageTreeQuery resolves a reference to a manifest and, for an index, the
// children it selects between.
func imageTreeQuery(fields string) string {
	return `query($repo: String!, $ref: String!) {
		image(repository: $repo, reference: $ref) {
			` + fields + `
			tags
			manifests {
				digest mediaType size
				platform { os architecture variant osVersion osFeatures }
				annotations { key value }
				manifest { ` + fields + ` }
			}
		}
	}`
}

var listTagsQuery = `query($repo: String!, $after: String) {
	repository(name: $repo) {
		tags(first: ` + fmt.Sprint(queryPage) + `, after: $after) {
			name
			manifest {
				` + imageFields + `
				manifests {
					digest mediaType size
					platform { os architecture variant osVersion osFeatures }
					annotations { key value }
					manifest { ` + imageFields + ` }
				}
			}
		}
	}
}`

var listRepositoriesQuery = `query($after: String) {
	repositories(first: ` + fmt.Sprint(queryPage) + `, after: $after) { name }
}`

// Images lists the tagged images in the caller's namespace from the metadata
// indexes. Index images are summarised by the manifest for the default
// platform; tags whose image content was never pulled are omitted.
func (r *RegistryService) Images(ctx context.Context, _ types.ImageListOptions) ([]imagetypes.Summary, error) {
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
		var after *string
		for {
			var data struct {
				Repository *struct {
					Tags []queryTag `json:"tags"`
				} `json:"repository"`
			}
			if err := r.Query(ctx, listTagsQuery, map[string]any{"repo": repo, "after": after}, &data); err != nil {
				return nil, err
			}
			if data.Repository == nil {
				break
			}
			for _, tag := range data.Repository.Tags {
				if summary, ok := imageSummary(id.Namespace, repo, tag); ok {
					summaries = append(summaries, summary)
				}
			}
			if len(data.Repository.Tags) < queryPage {
				break
			}
			last := data.Repository.Tags[len(data.Repository.Tags)-1].Name
			after = &last
		}
	}
	return summaries, nil
}

func (r *RegistryService) namespaceRepositories(ctx context.Context, namespace string) ([]string, error) {
	prefix := ""
	var after *string
	if namespace != "" {
		prefix = namespace + "/"
		// Repositories sort by name, so start just before the namespace.
		start := namespace + "."
		after = &start
	}
	var result []string
	for {
		var data struct {
			Repositories []struct {
				Name string `json:"name"`
			} `json:"repositories"`
		}
		if err := r.Query(ctx, listRepositoriesQuery, map[string]any{"after": after}, &data); err != nil {
			return nil, err
		}
		for _, repo := range data.Repositories {
			if !strings.HasPrefix(repo.Name, prefix) {
				if repo.Name > prefix {
					return result, nil
				}
				continue
			}
			result = append(result, repo.Name)
		}
		if len(data.Repositories) < queryPage {
			return result, nil
		}
		last := data.Repositories[len(data.Repositories)-1].Name
		after = &last
	}
}

// imageSummary describes the image a tag names. An image stored under the tag
// form of its digest is reported as a repository digest instead of a tag,
// which is how it was pulled.
func imageSummary(namespace, repo string, tag queryTag) (imagetypes.Summary, bool) {
	if tag.Manifest == nil {
		return imagetypes.Summary{}, false
	}
	root := tag.Manifest
	image := &root.queryImage
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
		platform.OS = deref(config.OS)
		platform.Architecture = deref(config.Architecture)
		platform.Variant = deref(config.Variant)
	}

	display := strings.TrimPrefix(repo, namespace+"/")
	name := display + ":" + tag.Name
	digestRef := display + "@" + root.Digest
	if _, byDigest := digestForTag(tag.Name); byDigest {
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
func presentPlatformChild(mediaType string, children []queryIndexChild) (queryIndexChild, bool) {
	var present []queryIndexChild
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
	return queryIndexChild{}, false
}

func defaultPlatformChild(mediaType string, children []queryIndexChild) (queryIndexChild, bool) {
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
		return queryIndexChild{}, false
	}
	for _, child := range children {
		if oci.Digest(child.Digest) == selected.platformDigest {
			return child, true
		}
	}
	return queryIndexChild{}, false
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

const removeImageQuery = `query($repo: String!, $ref: String!) {
	image(repository: $repo, reference: $ref) {
		digest
		tags
		closure { digest role retainedInRepository shared }
	}
}`

type closureEntry struct {
	Digest               string `json:"digest"`
	Role                 string `json:"role"`
	RetainedInRepository bool   `json:"retainedInRepository"`
	Shared               bool   `json:"shared"`
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

	var data struct {
		Image *struct {
			Digest  string         `json:"digest"`
			Tags    []string       `json:"tags"`
			Closure []closureEntry `json:"closure"`
		} `json:"image"`
	}
	if err := r.Query(ctx, removeImageQuery, map[string]any{"repo": resolved.repository, "ref": resolved.digest.String()}, &data); err != nil {
		return nil, err
	}
	if data.Image == nil {
		return nil, httpx.NotFound(fmt.Errorf("no such image: %s", name))
	}
	image := data.Image

	// A tagged name removes just that tag. An ID or digest names the image
	// itself, which docker only removes unforced while a single tag holds it.
	remove := []string{resolved.tag}
	if resolved.tag == "" {
		remove = image.Tags
		if len(remove) > 1 && !options.Force {
			return nil, httpx.Conflict(fmt.Errorf("unable to delete %s (must be forced) - image is referenced in multiple tags", name))
		}
	}

	records := make([]imagetypes.DeleteResponse, 0, len(remove))
	for _, tag := range remove {
		untagged := resolved.display() + ":" + tag
		if _, byDigest := digestForTag(tag); byDigest {
			untagged = resolved.display() + "@" + image.Digest
		}
		if err := local.DeleteTag(ctx, resolved.repository, tag); err != nil {
			return records, fmt.Errorf("untagging %s: %w", untagged, err)
		}
		records = append(records, imagetypes.DeleteResponse{Untagged: untagged})
	}

	remaining := 0
	for _, tag := range image.Tags {
		if !slices.Contains(remove, tag) {
			remaining++
		}
	}
	if remaining > 0 || len(image.Closure) == 0 || image.Closure[0].RetainedInRepository {
		return records, nil
	}

	// Manifests go first, parents before children, so every blob is
	// unreferenced in the repository by the time it is removed.
	var deleted []closureEntry
	for _, pass := range []bool{true, false} {
		for _, entry := range image.Closure {
			if (entry.Role == "MANIFEST") != pass || entry.RetainedInRepository {
				continue
			}
			digest := oci.Digest(entry.Digest)
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
			records = append(records, imagetypes.DeleteResponse{Deleted: entry.Digest})
		}
	}
	return records, nil
}
