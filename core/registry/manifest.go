package registry

import (
	"fmt"

	"github.com/containerd/platforms"
	"github.com/docker/oci"
)

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
