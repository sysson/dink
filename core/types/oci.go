package types

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ociref"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	imagetypes "github.com/moby/moby/api/types/image"
)

// Annotation keys Docker sets on index entries to link an attestation
// manifest to the image manifest it describes.
const (
	AnnotationReferenceType   = "vnd.docker.reference.type"
	AnnotationReferenceDigest = "vnd.docker.reference.digest"

	AnnotationReferenceTypeAttestation = "attestation-manifest"
)

type Digest struct {
	ocidigest.Digest
}

func (d Digest) ToOCI() digest.Digest {
	return digest.Digest(d.String())
}

// ID renders a digest the way docker identifies a layer in progress output:
// the first 12 characters of the encoded digest, without the algorithm.
func (d Digest) ID() string {
	const maxLen = 12
	s := d.Encoded()
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

type Platform struct {
	*oci.Platform
}

func (p Platform) ToOCI() *ocispec.Platform {
	if p.Platform == nil {
		return nil
	}
	return &ocispec.Platform{
		Architecture: p.Architecture,
		OS:           p.OS,
		OSVersion:    p.OSVersion,
		OSFeatures:   p.OSFeatures,
		Variant:      p.Variant,
	}
}

type Descriptor struct {
	oci.Descriptor
}

func (d Descriptor) ToOCI() ocispec.Descriptor {
	return ocispec.Descriptor{
		MediaType:    d.MediaType,
		Digest:       Digest{d.Digest}.ToOCI(),
		Size:         d.Size,
		URLs:         d.URLs,
		Annotations:  d.Annotations,
		Platform:     Platform{d.Platform}.ToOCI(),
		ArtifactType: d.ArtifactType,
		Data:         d.Data,
	}
}

// DescriptorsToOCI converts a slice of oci.Descriptor to their
// opencontainers/image-spec equivalents.
func DescriptorsToOCI(descs []oci.Descriptor) []ocispec.Descriptor {
	out := make([]ocispec.Descriptor, len(descs))
	for i, d := range descs {
		out[i] = Descriptor{d}.ToOCI()
	}
	return out
}

// Manifest is a manifest or index resolved from a registry: the descriptor
// that names it, the raw content of the blob it describes, and the
// descriptors decoded from that content.
type Manifest struct {
	oci.Descriptor
	// Contents is the raw JSON of the manifest or index blob.
	Contents []byte
	// Config is the config blob descriptor of an image manifest. Nil for an
	// index.
	Config *oci.Descriptor
	// Layers are the layer blob descriptors of an image manifest. Empty for
	// an index.
	Layers []oci.Descriptor
	// Manifests are the child descriptors of an index, carrying the platform
	// each child was built for. Empty for an image manifest.
	Manifests []oci.Descriptor
}

// DecodeManifest reads a manifest or index blob described by desc from r,
// decoding the descriptors referenced by its contents.
func DecodeManifest(desc oci.Descriptor, r io.Reader) (Manifest, error) {
	contents, err := io.ReadAll(r)
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest %s: %w", desc.Digest, err)
	}
	var decoded oci.IndexOrManifest
	if err := json.Unmarshal(contents, &decoded); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest %s: %w", desc.Digest, err)
	}
	return Manifest{
		Descriptor: desc,
		Contents:   contents,
		Config:     decoded.Config,
		Layers:     decoded.Layers,
		Manifests:  decoded.Manifests,
	}, nil
}

// IsIndex reports whether m is an image index rather than a single image
// manifest.
func (m Manifest) IsIndex() bool { return len(m.Manifests) > 0 }

// Kind reports the moby manifest kind for m, derived from its
// AnnotationReferenceType annotation.
func (m Manifest) Kind() imagetypes.ManifestKind {
	switch {
	case m.Annotations[AnnotationReferenceType] == AnnotationReferenceTypeAttestation:
		return imagetypes.ManifestKindAttestation
	case m.Config != nil:
		return imagetypes.ManifestKindImage
	default:
		return imagetypes.ManifestKindUnknown
	}
}

// ToManifestSummary converts m to the moby manifest summary returned from
// image queries. available reports whether the manifest's content is fully
// present locally, which cannot be derived from the manifest alone.
func (m Manifest) ToManifestSummary(available bool) imagetypes.ManifestSummary {
	summary := imagetypes.ManifestSummary{
		ID:         m.Digest.String(),
		Descriptor: Descriptor{m.Descriptor}.ToOCI(),
		Available:  available,
		Kind:       m.Kind(),
	}
	switch summary.Kind {
	case imagetypes.ManifestKindImage:
		platform := Platform{m.Platform}.ToOCI()
		if platform == nil {
			platform = &ocispec.Platform{}
		}
		summary.ImageData = &imagetypes.ImageProperties{Platform: *platform}
	case imagetypes.ManifestKindAttestation:
		summary.AttestationData = &imagetypes.AttestationProperties{
			For: digest.Digest(m.Annotations[AnnotationReferenceDigest]),
		}
	}
	return summary
}

// Reference wraps an ociref.Reference with docker's familiar-form string
// rendering.
type Reference struct {
	ociref.Reference
}

// ID is what docker labels the pull with: the tag, or the full digest when
// the reference names one.
func (r Reference) ID() string {
	var buf strings.Builder
	digestString := r.Digest.String()
	ref := r.Reference
	if ref.Host == "docker.io" {
		ref.Host = ""
		ref.Repository = strings.TrimPrefix(ref.Repository, "library/")
	}
	buf.Grow(len(ref.Host) + 1 + len(ref.Repository) + 1 + len(ref.Tag) + 1 + len(digestString))
	if ref.Host != "" {
		buf.WriteString(ref.Host)
		buf.WriteByte('/')
	}
	buf.WriteString(ref.Repository)
	if len(ref.Tag) > 0 {
		buf.WriteByte(':')
		buf.WriteString(ref.Tag)
	}
	if digestString != "" {
		buf.WriteByte('@')
		buf.WriteString(digestString)
	}
	return buf.String()
}
