package registry

import (
	"strings"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/types"
)

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
func repositoryFor(id identity.Identity, ref types.Reference) types.Reference {
	var repo string
	if ref.Host == "docker.io" {
		repo = id.Namespace + "/" + strings.TrimPrefix(ref.Repository, "library/")
	} else {
		repo = id.Namespace + "/" + repositoryHost(ref.Host) + "/" + ref.Repository
	}
	return types.Reference{
		Repository: repo,
		Tag:        ref.Tag,
		Digest:     ref.Digest,
	}
}

// repositoryHost renders a registry host as a repository path component.
// "host:port" is a valid registry address but the port separator is not
// valid in an OCI repository name, so it is replaced.
func repositoryHost(host string) string {
	return strings.ReplaceAll(host, ":", "-")
}

// digestTag renders a digest as the tag an image pulled by digest is stored
// under. The registry API cannot enumerate untagged manifests, so without a
// tag such an image could neither be listed nor kept from being garbage
// collected.
func digestTag(d oci.Digest) string {
	return d.Algorithm().String() + "-" + d.Encoded()
}

// digestForTag returns the digest a tag written by digestTag names, and
// whether the tag is one.
func digestForTag(tag string) (oci.Digest, bool) {
	algorithm, encoded, ok := strings.Cut(tag, "-")
	if !ok {
		return "", false
	}
	d, err := ocidigest.Parse(algorithm + ":" + encoded)
	if err != nil {
		return "", false
	}
	return d, true
}
