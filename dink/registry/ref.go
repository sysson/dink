package registry

import (
	"strings"

	"github.com/docker/oci"
	"github.com/docker/oci/ociref"
	"github.com/sysson/dink/dink/identity"
)

// sourceRef holds the parsed pieces of an image reference as provided by the
// user (e.g. from `docker pull`). Exactly one of Tag and Digest is set.
type sourceRef struct {
	ociref.Reference
}

// newSourceRef splits an already-parsed reference into the pieces a pull
// needs. [ociref.ParseRelative] has, at this point, already defaulted the
// host to docker.io and applied the "library" namespace to single-component
// Docker Hub names, matching `docker pull` semantics.
func newSourceRef(ref ociref.Reference) sourceRef {
	// A digest pins the image, so it takes precedence over any tag given
	// alongside it, as `docker pull repo:tag@digest` does.
	src := sourceRef{Host: ref.Host, Repository: ref.Repository, Digest: ref.Digest}
	if src.Digest == "" {
		src.Tag = ref.Tag
		if src.Tag == "" {
			src.Tag = "latest"
		}
	}
	return src
}

// String renders the reference the way docker echoes it back to the client.
func (r sourceRef) String() string {
	if r.Digest != "" {
		return r.Repository + "@" + r.Digest.String()
	}
	return r.Repository + ":" + r.Tag
}

// progressID is the identifier docker clients display for the pull as a
// whole, as opposed to for an individual layer.
func (r sourceRef) progressID() string {
	if r.Digest != "" {
		return ShortDigest(r.Digest)
	}
	return r.Tag
}

// repositoryFor returns the internal repository path that a pulled image is
// stored under for the given tenant identity. The path namespaces the image
// by the tenant and the registry host it came from, so that pulling the same
// repository from different registries (e.g. docker.io/nginx vs
// ghcr.io/nginx) produces distinct internal repositories:
//
//	<namespace>/<host>/<repository>
//
// For example, with a tenant namespace "dev", `docker pull nginx:latest`
// resolves to "dev/docker.io/library/nginx:latest".
func repositoryFor(id identity.Identity, ref sourceRef) string {
	return id.Namespace + "/" + repositoryHost(ref.Host) + "/" + ref.Repository
}

// repositoryHost renders a registry host as a repository path component.
// "host:port" is a valid registry address but the port separator is not
// valid in an OCI repository name, so it is replaced.
func repositoryHost(host string) string {
	return strings.ReplaceAll(host, ":", "-")
}

// ShortDigest returns the truncated form of a digest suitable for display in
// progress output (e.g. "sha256:1a2b3c4d5e6f").
func ShortDigest(digest oci.Digest) string {
	const maxLen = 19
	s := digest.String()
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
