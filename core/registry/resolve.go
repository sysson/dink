package registry

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ociref"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/syskit/httpx"
)

// resolvedImage is an image stored in the caller's namespace, found either by
// the name that references it or by its image ID.
type resolvedImage struct {
	namespace  string
	repository string
	// digest is what the reference resolves to, which is the image ID.
	digest oci.Digest
	// tag is the tag the caller named, empty when the image was found by
	// digest or by ID.
	tag string
}

// display renders the repository the way a docker client names it.
func (i resolvedImage) display() string {
	return strings.TrimPrefix(i.repository, i.namespace+"/")
}

// reference renders the image the way a docker client names it.
func (i resolvedImage) reference() string {
	if _, byDigest := digestForTag(i.tag); i.tag == "" || byDigest {
		return i.display() + "@" + i.digest.String()
	}
	return i.display() + ":" + i.tag
}

const resolveImageQuery = `query($repo: String!, $ref: String!) {
	image(repository: $repo, reference: $ref) { digest }
}`

var repositoryTagsQuery = `query($repo: String!, $after: String) {
	repository(name: $repo) {
		tags(first: ` + fmt.Sprint(queryPage) + `, after: $after) { name digest }
	}
}`

// resolveImage finds the image name refers to in the caller's namespace.
// Following docker, name may be a repository with an optional tag
// ("nginx:latest"), a digest reference ("nginx@sha256:...") or a full or
// truncated image ID ("sha256:abc123..." or "abc123456789"). An ID is looked
// up first and a name that is also valid hex falls back to the repository of
// that name.
func (r *RegistryService) resolveImage(ctx context.Context, name string) (resolvedImage, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return resolvedImage{}, fmt.Errorf("missing identity in context")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return resolvedImage{}, httpx.BadRequest(errors.New("image name is required"))
	}

	if prefix, isID := imageIDPrefix(name); isID {
		image, found, err := r.imageByID(ctx, id.Namespace, prefix)
		if err != nil {
			return resolvedImage{}, err
		}
		if found {
			return image, nil
		}
	}

	parsed, err := ociref.ParseRelative(name)
	if err != nil {
		return resolvedImage{}, httpx.BadRequest(err)
	}
	ref := repositoryFor(id, parsed)
	reference, tag := ref.Tag, ref.Tag
	if parsed.Digest != "" {
		// Any manifest in the repository can be named by digest, not only
		// the ones stored under the tag form of their digest.
		reference, tag = parsed.Digest.String(), ""
	} else if reference == "" {
		reference, tag = "latest", "latest"
	}

	var data struct {
		Image *struct {
			Digest string `json:"digest"`
		} `json:"image"`
	}
	if err := r.Query(ctx, resolveImageQuery, map[string]any{"repo": ref.Repository, "ref": reference}, &data); err != nil {
		return resolvedImage{}, err
	}
	if data.Image == nil {
		return resolvedImage{}, httpx.NotFound(fmt.Errorf("no such image: %s", name))
	}
	return resolvedImage{
		namespace:  id.Namespace,
		repository: ref.Repository,
		digest:     oci.Digest(data.Image.Digest),
		tag:        tag,
	}, nil
}

// imageByID finds the tagged image in the namespace whose digest starts with
// prefix. Only tagged images are considered: they are the ones listed, and so
// the only ones whose ID a client can have seen.
func (r *RegistryService) imageByID(ctx context.Context, namespace, prefix string) (resolvedImage, bool, error) {
	repos, err := r.namespaceRepositories(ctx, namespace)
	if err != nil {
		return resolvedImage{}, false, err
	}
	var matches []resolvedImage
	for _, repo := range repos {
		var after *string
		for {
			var data struct {
				Repository *struct {
					Tags []struct {
						Name   string `json:"name"`
						Digest string `json:"digest"`
					} `json:"tags"`
				} `json:"repository"`
			}
			if err := r.Query(ctx, repositoryTagsQuery, map[string]any{"repo": repo, "after": after}, &data); err != nil {
				return resolvedImage{}, false, err
			}
			if data.Repository == nil {
				break
			}
			for _, tag := range data.Repository.Tags {
				digest := oci.Digest(tag.Digest)
				if !strings.HasPrefix(digest.Encoded(), prefix) {
					continue
				}
				match := resolvedImage{namespace: namespace, repository: repo, digest: digest}
				if !containsImage(matches, match) {
					matches = append(matches, match)
				}
			}
			if len(data.Repository.Tags) < queryPage {
				break
			}
			last := data.Repository.Tags[len(data.Repository.Tags)-1].Name
			after = &last
		}
	}
	switch len(matches) {
	case 0:
		return resolvedImage{}, false, nil
	case 1:
		return matches[0], true, nil
	default:
		return resolvedImage{}, false, httpx.BadRequest(fmt.Errorf("multiple images found with prefix %s", prefix))
	}
}

func containsImage(images []resolvedImage, image resolvedImage) bool {
	for _, existing := range images {
		if existing.repository == image.repository && existing.digest == image.digest {
			return true
		}
	}
	return false
}

// imageIDPrefix returns the hex prefix of the image ID name refers to, and
// whether it is one. Both the full "sha256:<hex>" form and the bare, possibly
// truncated hex that docker prints are accepted.
func imageIDPrefix(name string) (string, bool) {
	if strings.ContainsAny(name, "/@") {
		return "", false
	}
	value := name
	// Docker assumes sha256 when the algorithm is left off.
	size := ocidigest.SHA256.Size()
	if algorithm, encoded, ok := strings.Cut(name, ":"); ok {
		a, err := ocidigest.LookupAlgorithm(algorithm)
		if err != nil {
			return "", false
		}
		value, size = encoded, a.Size()
	}
	if len(value) > size*2 || !imageIDPattern.MatchString(value) {
		return "", false
	}
	return value, true
}

// imageIDPattern is the shortest prefix docker accepts for a truncated ID,
// and up.
var imageIDPattern = regexp.MustCompile(`^[0-9a-f]{4,}$`)
