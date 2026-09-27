package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/containerd/platforms"
	"github.com/docker/oci"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/syskit/httpx"
)

// inTotoPredicateTypeAnnotation is the layer annotation buildkit writes to
// record the in-toto predicate type of a statement in an attestation
// manifest.
const inTotoPredicateTypeAnnotation = "in-toto.io/predicate-type"

// maxStatementSize bounds how much of an attestation statement is read into
// memory; statements are small documents.
const maxStatementSize = 4 << 20

// ImageHistory returns the layers name was built from, newest first. Sizes
// are the sizes of the stored (compressed) layer blobs, which is all a
// registry-backed store knows.
func (r *RegistryService) ImageHistory(ctx context.Context, name string, platform *ocispec.Platform) ([]imagetypes.HistoryResponseItem, error) {
	resolved, tree, err := r.inspectTree(ctx, name)
	if err != nil {
		return nil, err
	}
	image, _, err := platformManifest(tree, platform)
	if err != nil {
		return nil, err
	}
	config, err := decodeImageConfig(resolved, image)
	if err != nil {
		return nil, err
	}

	items := make([]imagetypes.HistoryResponseItem, 0, len(config.History))
	layer := 0
	for _, entry := range config.History {
		var size int64
		if !entry.EmptyLayer {
			if layer < len(image.Layers) {
				size = image.Layers[layer].Size
			}
			layer++
		}
		var created int64
		if entry.Created != nil {
			created = entry.Created.Unix()
		}
		items = append(items, imagetypes.HistoryResponseItem{
			ID:        "<missing>",
			Created:   created,
			CreatedBy: entry.CreatedBy,
			Comment:   entry.Comment,
			Size:      size,
			Tags:      []string{},
		})
	}
	slices.Reverse(items)
	if len(items) > 0 {
		items[0].ID = tree.Digest
		items[0].Tags = repoTags(resolved, tree)
	}
	return items, nil
}

// ImageInspect describes the image name refers to. Fields a registry-backed
// store has no notion of, such as the graph driver and the parent chain, are
// left empty.
func (r *RegistryService) ImageInspect(ctx context.Context, name string, options imagebackend.ImageInspectOpts) (*imagebackend.InspectData, error) {
	resolved, tree, err := r.inspectTree(ctx, name)
	if err != nil {
		return nil, err
	}
	image, child, err := platformManifest(tree, options.Platform)
	if err != nil {
		return nil, err
	}
	config, err := decodeImageConfig(resolved, image)
	if err != nil {
		return nil, err
	}

	platform := ocispec.Platform{OS: config.OS, Architecture: config.Architecture, Variant: config.Variant, OSVersion: config.OSVersion}
	if child != nil && child.Platform != nil {
		platform = ociPlatform(*child.Platform)
	}
	response := imagetypes.InspectResponse{
		ID:           tree.Digest,
		RepoTags:     repoTags(resolved, tree),
		RepoDigests:  []string{resolved.display() + "@" + tree.Digest},
		Author:       config.Author,
		Config:       &config.Config,
		Architecture: platform.Architecture,
		Variant:      platform.Variant,
		Os:           platform.OS,
		OsVersion:    platform.OSVersion,
		Size:         imageSize(tree, image),
		RootFS:       imagetypes.RootFS{Type: "layers", Layers: diffIDs(config)},
		Descriptor: &ocispec.Descriptor{
			MediaType: tree.MediaType,
			Digest:    digest.Digest(tree.Digest),
			Size:      tree.Size,
			Platform:  &platform,
		},
	}
	if config.Created != nil {
		response.Created = config.Created.Format(time.RFC3339Nano)
	}
	if options.Manifests {
		response.Manifests = manifestSummaries(tree)
	}
	return &imagebackend.InspectData{InspectResponse: response}, nil
}

// ImageAttestations returns the in-toto statements attached to the platform
// image of name. Statements are carried by a sibling manifest in the image
// index, the way buildkit attaches them, so an image that is not an index
// never has any.
func (r *RegistryService) ImageAttestations(ctx context.Context, name string, options imagebackend.AttestationOpts) ([]imagetypes.AttestationStatement, error) {
	local, err := r.client()
	if err != nil {
		return nil, err
	}
	resolved, tree, err := r.inspectTree(ctx, name)
	if err != nil {
		return nil, err
	}
	if !isIndex(tree.MediaType) {
		return nil, nil
	}
	_, child, err := platformManifest(tree, options.Platform)
	if err != nil {
		return nil, err
	}
	if child == nil {
		return nil, nil
	}
	attestation := attestationManifest(tree, child.Digest)
	if attestation == nil {
		return nil, nil
	}

	var statements []imagetypes.AttestationStatement
	for _, layer := range attestation.Layers {
		predicate := annotationValue(layer.Annotations, inTotoPredicateTypeAnnotation)
		if predicate == "" {
			continue
		}
		if len(options.PredicateTypes) > 0 && !slices.Contains(options.PredicateTypes, predicate) {
			continue
		}
		statement := imagetypes.AttestationStatement{
			Descriptor: ocispec.Descriptor{
				MediaType:   layer.MediaType,
				Digest:      digest.Digest(layer.Digest),
				Size:        layer.Size,
				Annotations: annotationMap(layer.Annotations),
			},
			PredicateType: predicate,
		}
		if options.IncludeStatement {
			data, err := readStatement(ctx, local, resolved.repository, oci.Digest(layer.Digest), layer.Size)
			if err != nil {
				return nil, fmt.Errorf("reading attestation statement %s: %w", layer.Digest, err)
			}
			raw := json.RawMessage(data)
			statement.Statement = &raw
		}
		statements = append(statements, statement)
	}
	return statements, nil
}

// inspectTree resolves name and loads the manifest tree it names.
func (r *RegistryService) inspectTree(ctx context.Context, name string) (resolvedImage, *queryImageTree, error) {
	resolved, err := r.resolveImage(ctx, name)
	if err != nil {
		return resolvedImage{}, nil, err
	}
	var data struct {
		Image *queryImageTree `json:"image"`
	}
	variables := map[string]any{"repo": resolved.repository, "ref": resolved.digest.String()}
	if err := r.Query(ctx, imageTreeQuery(inspectFields), variables, &data); err != nil {
		return resolvedImage{}, nil, err
	}
	if data.Image == nil {
		return resolvedImage{}, nil, httpx.NotFound(fmt.Errorf("no such image: %s", name))
	}
	return resolved, data.Image, nil
}

// platformManifest returns the image manifest describing tree for platform,
// and the index child it came from when tree is an index. A nil platform
// selects the daemon's default platform among the manifests that were pulled.
func platformManifest(tree *queryImageTree, platform *ocispec.Platform) (*queryImage, *queryIndexChild, error) {
	if !isIndex(tree.MediaType) {
		return &tree.queryImage, nil, nil
	}
	if platform != nil {
		matcher := platforms.Only(*platform)
		for _, child := range tree.Manifests {
			if child.Manifest == nil || child.Platform == nil {
				continue
			}
			if matcher.Match(ociPlatform(*child.Platform)) {
				return child.Manifest, &child, nil
			}
		}
		return nil, nil, httpx.NotFound(fmt.Errorf("image does not contain the requested platform %s", platforms.FormatAll(*platform)))
	}
	child, ok := presentPlatformChild(tree.MediaType, tree.Manifests)
	if !ok {
		return nil, nil, httpx.NotFound(errors.New("image has no stored platform manifest"))
	}
	return child.Manifest, &child, nil
}

// attestationManifest returns the manifest holding the statements attached to
// the image manifest named by digest, if the index carries one.
func attestationManifest(tree *queryImageTree, digest string) *queryImage {
	for _, child := range tree.Manifests {
		if annotationValue(child.Annotations, AnnotationReferenceType) != AnnotationReferenceTypeAttestation {
			continue
		}
		if annotationValue(child.Annotations, AnnotationReferenceDigest) != digest {
			continue
		}
		return child.Manifest
	}
	return nil
}

func decodeImageConfig(resolved resolvedImage, image *queryImage) (*dockerspec.DockerOCIImage, error) {
	if image == nil || image.ImageConfig == nil || image.ImageConfig.Raw == "" {
		return nil, httpx.NotFound(fmt.Errorf("image config for %s is not stored locally", resolved.reference()))
	}
	var config dockerspec.DockerOCIImage
	if err := json.Unmarshal([]byte(image.ImageConfig.Raw), &config); err != nil {
		return nil, fmt.Errorf("parsing image config for %s: %w", resolved.reference(), err)
	}
	return &config, nil
}

// repoTags lists the names in the repository that reference the image.
// Images stored under the tag form of their digest were pulled by digest and
// carry no tag.
func repoTags(resolved resolvedImage, tree *queryImageTree) []string {
	tags := make([]string, 0, len(tree.Tags))
	for _, tag := range tree.Tags {
		if _, byDigest := digestForTag(tag); byDigest {
			continue
		}
		tags = append(tags, resolved.display()+":"+tag)
	}
	return tags
}

func diffIDs(config *dockerspec.DockerOCIImage) []string {
	layers := make([]string, 0, len(config.RootFS.DiffIDs))
	for _, diffID := range config.RootFS.DiffIDs {
		layers = append(layers, diffID.String())
	}
	return layers
}

// imageSize is the size of everything the image is made of: the manifests
// naming it, its config and its layers.
func imageSize(tree *queryImageTree, image *queryImage) int64 {
	size := tree.Size
	if image.Digest != tree.Digest {
		size += image.Size
	}
	return size + manifestSize(image)
}

func manifestSize(image *queryImage) int64 {
	var size int64
	if image.Config != nil {
		size += image.Config.Size
	}
	for _, layer := range image.Layers {
		size += layer.Size
	}
	return size
}

func manifestSummaries(tree *queryImageTree) []imagetypes.ManifestSummary {
	summaries := make([]imagetypes.ManifestSummary, 0, len(tree.Manifests))
	for _, child := range tree.Manifests {
		descriptor := ocispec.Descriptor{
			MediaType:   child.MediaType,
			Digest:      digest.Digest(child.Digest),
			Size:        child.Size,
			Annotations: annotationMap(child.Annotations),
		}
		if child.Platform != nil {
			platform := ociPlatform(*child.Platform)
			descriptor.Platform = &platform
		}
		summary := imagetypes.ManifestSummary{
			ID:         child.Digest,
			Descriptor: descriptor,
			Available:  child.Manifest != nil,
			Kind:       imagetypes.ManifestKindUnknown,
		}
		if child.Manifest != nil {
			summary.Size.Content = child.Size + manifestSize(child.Manifest)
			summary.Size.Total = summary.Size.Content
		}
		switch {
		case annotationValue(child.Annotations, AnnotationReferenceType) == AnnotationReferenceTypeAttestation:
			summary.Kind = imagetypes.ManifestKindAttestation
			summary.AttestationData = &imagetypes.AttestationProperties{
				For: digest.Digest(annotationValue(child.Annotations, AnnotationReferenceDigest)),
			}
		case descriptor.Platform != nil && descriptor.Platform.OS != "unknown":
			summary.Kind = imagetypes.ManifestKindImage
			summary.ImageData = &imagetypes.ImageProperties{Platform: *descriptor.Platform, Containers: []string{}}
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

func readStatement(ctx context.Context, client oci.Interface, repository string, digest oci.Digest, size int64) ([]byte, error) {
	if size > maxStatementSize {
		return nil, fmt.Errorf("statement is %d bytes, larger than the %d byte limit", size, maxStatementSize)
	}
	blob, err := client.GetBlob(ctx, repository, digest)
	if err != nil {
		if isNotFound(err) {
			return nil, httpx.NotFound(err)
		}
		return nil, err
	}
	defer func() { _ = blob.Close() }()
	return io.ReadAll(io.LimitReader(blob, maxStatementSize))
}

func ociPlatform(platform queryPlatform) ocispec.Platform {
	return ocispec.Platform{
		OS:           platform.OS,
		Architecture: platform.Architecture,
		Variant:      deref(platform.Variant),
		OSVersion:    deref(platform.OSVersion),
		OSFeatures:   platform.OSFeatures,
	}
}

func annotationValue(annotations []queryAnnotation, key string) string {
	for _, annotation := range annotations {
		if annotation.Key == key {
			return annotation.Value
		}
	}
	return ""
}

func annotationMap(annotations []queryAnnotation) map[string]string {
	if len(annotations) == 0 {
		return nil
	}
	result := make(map[string]string, len(annotations))
	for _, annotation := range annotations {
		result[annotation.Key] = annotation.Value
	}
	return result
}
