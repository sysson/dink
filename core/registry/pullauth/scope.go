package pullauth

import (
	"context"
	"iter"
	"strings"

	"github.com/docker/oci"
)

// Scope returns a read-only view of r limited to the repositories of the
// namespace in each request's context. Other repositories, and every
// repository when the context has no namespace, are reported as unknown.
func Scope(r oci.Interface) oci.Interface {
	return &scoped{Funcs: &oci.Funcs{}, r: r}
}

// scoped embeds empty Funcs so every mutating method is unsupported.
type scoped struct {
	*oci.Funcs
	r oci.Interface
}

func allowed(ctx context.Context, repo string) bool {
	namespace, ok := FromContext(ctx)
	return ok && strings.HasPrefix(repo, namespace+"/")
}

func (s *scoped) GetBlob(ctx context.Context, repo string, digest oci.Digest) (oci.BlobReader, error) {
	if !allowed(ctx, repo) {
		return nil, oci.ErrNameUnknown
	}
	return s.r.GetBlob(ctx, repo, digest)
}

func (s *scoped) GetBlobRange(ctx context.Context, repo string, digest oci.Digest, offset0, offset1 int64) (oci.BlobReader, error) {
	if !allowed(ctx, repo) {
		return nil, oci.ErrNameUnknown
	}
	return s.r.GetBlobRange(ctx, repo, digest, offset0, offset1)
}

func (s *scoped) GetManifest(ctx context.Context, repo string, digest oci.Digest) (oci.BlobReader, error) {
	if !allowed(ctx, repo) {
		return nil, oci.ErrNameUnknown
	}
	return s.r.GetManifest(ctx, repo, digest)
}

func (s *scoped) GetTag(ctx context.Context, repo, tagName string) (oci.BlobReader, error) {
	if !allowed(ctx, repo) {
		return nil, oci.ErrNameUnknown
	}
	return s.r.GetTag(ctx, repo, tagName)
}

func (s *scoped) ResolveBlob(ctx context.Context, repo string, digest oci.Digest) (oci.Descriptor, error) {
	if !allowed(ctx, repo) {
		return oci.Descriptor{}, oci.ErrNameUnknown
	}
	return s.r.ResolveBlob(ctx, repo, digest)
}

func (s *scoped) ResolveManifest(ctx context.Context, repo string, digest oci.Digest) (oci.Descriptor, error) {
	if !allowed(ctx, repo) {
		return oci.Descriptor{}, oci.ErrNameUnknown
	}
	return s.r.ResolveManifest(ctx, repo, digest)
}

func (s *scoped) ResolveTag(ctx context.Context, repo, tagName string) (oci.Descriptor, error) {
	if !allowed(ctx, repo) {
		return oci.Descriptor{}, oci.ErrNameUnknown
	}
	return s.r.ResolveTag(ctx, repo, tagName)
}

func (s *scoped) Tags(ctx context.Context, repo string, params *oci.TagsParameters) iter.Seq2[string, error] {
	if !allowed(ctx, repo) {
		return oci.ErrorSeq[string](oci.ErrNameUnknown)
	}
	return s.r.Tags(ctx, repo, params)
}

func (s *scoped) Referrers(ctx context.Context, repo string, digest oci.Digest, params *oci.ReferrersParameters) iter.Seq2[oci.Descriptor, error] {
	if !allowed(ctx, repo) {
		return oci.ErrorSeq[oci.Descriptor](oci.ErrNameUnknown)
	}
	return s.r.Referrers(ctx, repo, digest, params)
}

// Repositories lists only the namespace's repositories.
func (s *scoped) Repositories(ctx context.Context, startAfter string) iter.Seq2[string, error] {
	namespace, ok := FromContext(ctx)
	if !ok {
		return func(func(string, error) bool) {}
	}
	prefix := namespace + "/"
	// '.' sorts just before '/', so this starts at the namespace's first repository.
	startAfter = max(startAfter, namespace+".")
	return func(yield func(string, error) bool) {
		for repo, err := range s.r.Repositories(ctx, startAfter) {
			if err != nil {
				yield("", err)
				return
			}
			if !strings.HasPrefix(repo, prefix) {
				return
			}
			if !yield(repo, nil) {
				return
			}
		}
	}
}
