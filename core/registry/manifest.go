package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"

	"github.com/containerd/platforms"
	"github.com/docker/oci"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// fetchedManifest is a manifest read from the source registry, together with
// the descriptors decoded from its contents.
type fetchedManifest struct {
	desc     oci.Descriptor
	contents []byte
	// config is the config blob descriptor of an image manifest.
	config *oci.Descriptor
	// layers are the layer blob descriptors of an image manifest.
	layers []oci.Descriptor
	// manifests are the child descriptors of an index, carrying the platform
	// each child was built for. It is empty for an image manifest.
	manifests []oci.Descriptor
}

// isIndex reports whether the manifest is an image index rather than a single
// image manifest.
func (m fetchedManifest) isIndex() bool { return len(m.manifests) > 0 }

type childManifest struct {
	descriptor oci.Descriptor
	tags       []string
}

type childManifests []childManifest

// childManifestsForPlatform returns the manifests to copy for a tagged image
// index: the selected platform manifest (tagged) plus any attestation
// manifests that reference it.
func childManifestsForPlatform(manifests []oci.Descriptor, selected oci.Descriptor, tags []string) childManifests {
	children := childManifests{{descriptor: selected, tags: tags}}
	for _, m := range manifests {
		if m.Annotations["vnd.docker.reference.type"] != "attestation-manifest" {
			continue
		}
		if m.Annotations["vnd.docker.reference.digest"] != selected.Digest.String() {
			continue
		}
		children = append(children, childManifest{descriptor: m})
	}
	return children
}

// allChildManifests returns every manifest referenced by an index, untagged,
// so that the index can be reproduced verbatim in the destination.
func allChildManifests(manifests []oci.Descriptor) childManifests {
	children := make(childManifests, 0, len(manifests))
	for _, m := range manifests {
		children = append(children, childManifest{descriptor: m})
	}
	return children
}

// selectManifest picks the child manifest matching the configured platform
// from an image index, defaulting to the current runtime platform.
func selectManifest(p platforms.MatchComparer, manifests []oci.Descriptor) (oci.Descriptor, error) {
	matcher := p
	if matcher == nil {
		matcher = platforms.Default()
	}
	for _, m := range manifests {
		if m.Platform == nil {
			continue
		}
		if matcher.Match(ociPlatformToSpec(*m.Platform)) {
			return m, nil
		}
	}
	return oci.Descriptor{}, fmt.Errorf("no manifest found for platform %s", matcherString(matcher))
}

// platformMatcher returns a matcher for the first requested platform, or nil
// to use the runtime default.
func platformMatcher(pls []ocispec.Platform) platforms.MatchComparer {
	if len(pls) == 0 {
		return nil
	}
	return platforms.Only(pls[0])
}

// ociPlatformToSpec converts an oci.Platform to the OCI image-spec platform
// used by platform matchers.
func ociPlatformToSpec(p oci.Platform) ocispec.Platform {
	return ocispec.Platform{
		Architecture: p.Architecture,
		OS:           p.OS,
		OSVersion:    p.OSVersion,
		OSFeatures:   p.OSFeatures,
		Variant:      p.Variant,
	}
}

func matcherString(m platforms.MatchComparer) string {
	if m == nil {
		return runtime.GOOS + "/" + runtime.GOARCH
	}
	return fmt.Sprintf("%v", m)
}

// resolveManifest resolves the tag or digest in req to a concrete manifest in
// the source registry, and returns it along with the tags to apply to the copy
// in the destination repository.
func (c *Copier) resolveManifest(ctx context.Context, req CopyRequest) (fetchedManifest, []string, error) {
	var (
		reader oci.BlobReader
		tags   = req.DstTags
		err    error
	)
	if req.SrcDigest != "" {
		reader, err = c.Src.GetManifest(ctx, req.SrcRepo, req.SrcDigest)
	} else {
		srcTag := req.SrcTag
		if srcTag == "" {
			srcTag = "latest"
		}
		if len(tags) == 0 {
			tags = []string{srcTag}
		}
		reader, err = c.Src.GetTag(ctx, req.SrcRepo, srcTag)
	}
	if err != nil {
		return fetchedManifest{}, nil, err
	}
	m, err := readManifest(reader)
	if err != nil {
		return fetchedManifest{}, nil, err
	}
	return m, tags, nil
}

// fetchManifest reads the manifest with the given digest from the source
// registry.
func (c *Copier) fetchManifest(ctx context.Context, repo string, digest oci.Digest) (fetchedManifest, error) {
	reader, err := c.Src.GetManifest(ctx, repo, digest)
	if err != nil {
		return fetchedManifest{}, fmt.Errorf("get manifest %s: %w", digest, err)
	}
	return readManifest(reader)
}

// readManifest reads a manifest and decodes the descriptors a copy needs from
// it: the config and layer blobs of an image manifest, or the child manifests
// and their platforms for an index.
func readManifest(reader oci.BlobReader) (m fetchedManifest, err error) {
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	m.desc = reader.Descriptor()
	m.contents, err = io.ReadAll(reader)
	if err != nil {
		return fetchedManifest{}, fmt.Errorf("read manifest %s: %w", m.desc.Digest, err)
	}
	var decoded oci.IndexOrManifest
	if err := json.Unmarshal(m.contents, &decoded); err != nil {
		return fetchedManifest{}, fmt.Errorf("decode manifest %s: %w", m.desc.Digest, err)
	}
	m.config = decoded.Config
	m.layers = decoded.Layers
	m.manifests = decoded.Manifests
	return m, nil
}

// copyManifest copies m and everything it references: its child manifests,
// then its config blob, then its layer blobs, and finally the manifest itself,
// so that the manifest only becomes resolvable once its content is present.
func (c *Copier) copyManifest(ctx context.Context, repos repoPair, m fetchedManifest, tags []string) error {
	children := childManifests{{descriptor: m.desc, tags: tags}}
	switch {
	case m.isIndex() && len(tags) > 0:
		selected, err := selectManifest(c.Platform, m.manifests)
		if err != nil {
			return err
		}
		children = childManifestsForPlatform(m.manifests, selected, tags)
	case m.isIndex():
		children = allChildManifests(m.manifests)
	}

	for _, child := range children {
		if child.descriptor.Digest == m.desc.Digest {
			continue
		}
		childManifest, err := c.fetchManifest(ctx, repos.SrcRepo, child.descriptor.Digest)
		if err != nil {
			return err
		}
		if err := c.copyManifest(ctx, repos, childManifest, child.tags); err != nil {
			return err
		}
	}
	// A tagged index is flattened: the selected child carries the tag, so the
	// index itself is dropped. An untagged index is preserved in full, since
	// dropping platforms would change its bytes and so its digest.
	if m.isIndex() && len(tags) > 0 {
		return nil
	}

	if m.config != nil {
		config := blobCopy{desc: *m.config, completeStatus: "Download complete", report: false}
		if err := c.copyBlob(ctx, repos, config); err != nil {
			return err
		}
	}
	if err := c.copyLayers(ctx, repos, m.layers); err != nil {
		return err
	}

	if _, err := c.Dst.PushManifest(ctx, repos.DstRepo, m.contents, m.desc.MediaType, &oci.PushManifestParameters{
		Digest: m.desc.Digest,
		Tags:   tags,
	}); err != nil {
		return fmt.Errorf("push manifest %s: %w", m.desc.Digest, err)
	}
	return nil
}
