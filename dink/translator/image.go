package translator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"

	"github.com/docker/oci"
	"github.com/docker/oci/ociauth"
	"github.com/docker/oci/ociclient"
	"github.com/docker/oci/ocimem"
	"github.com/docker/oci/ociref"
	"github.com/moby/moby/api/types/jsonstream"
)

const internalRegistry = "localhost:5000"

var internalImageStore = ocimem.New()

func (d *Docker) ImageDelete()       {}
func (d *Docker) ImageHistory()      {}
func (d *Docker) Images()            {}
func (d *Docker) GetImage()          {}
func (d *Docker) ImageInspect()      {}
func (d *Docker) ImageAttestations() {}
func (d *Docker) TagImage()          {}
func (d *Docker) ImagePrune()        {}
func (d *Docker) LoadImage()         {}
func (d *Docker) ImportImage()       {}
func (d *Docker) ExportImage()       {}

func (d *Docker) PullImage(ctx context.Context, fromImage, tag string, progress func(jsonstream.Message)) (string, error) {
	srcRegistry, srcRepo, srcTag, err := parseSourceRef(fromImage, tag)
	if err != nil {
		return "", err
	}
	dstRepo := internalRepo(srcRegistry, srcRepo)
	internalRef := internalRegistry + "/" + dstRepo + ":" + srcTag
	if cached, err := tagExists(ctx, internalImageStore, dstRepo, srcTag); err != nil {
		return "", err
	} else if cached {
		progress(jsonstream.Message{Status: "Image is up to date for " + fromImage})
		return internalRef, nil
	}

	progress(jsonstream.Message{Status: "Pulling from " + srcRegistry + "/" + srcRepo, ID: srcTag})
	digest, err := mirrorImage(ctx, internalImageStore, srcRegistry, srcRepo, dstRepo, srcTag, progress)
	if err != nil {
		return "", err
	}
	progress(jsonstream.Message{Status: "Digest: " + digest.String()})
	progress(jsonstream.Message{Status: "Downloaded newer image for " + fromImage})
	return internalRef, nil
}

func (d *Docker) PushImage() {}
func (d *Docker) Search()    {}

func mirrorImage(ctx context.Context, dst oci.Interface, registry, srcRepo, dstRepo, tag string, progress func(jsonstream.Message)) (oci.Digest, error) {
	cf, err := ociauth.Load(nil)
	if err != nil {
		return "", err
	}

	transport := ociauth.NewStdTransport(ociauth.StdTransportParams{Config: cf})
	if registry == "docker.io" || registry == "registry-1.docker.io" {
		transport = &dockerHubTokenTransport{base: transport, tokens: map[string]string{}}
	}

	src, err := ociclient.New(registry, &ociclient.Options{Transport: transport})
	if err != nil {
		return "", err
	}

	manifest, err := src.GetTag(ctx, srcRepo, tag)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := manifest.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	digest := manifest.Descriptor().Digest
	contents, err := io.ReadAll(manifest)
	if err != nil {
		return "", err
	}

	return digest, copyManifest(ctx, src, dst, srcRepo, dstRepo, manifest.Descriptor(), contents, []string{tag}, "Pull complete", progress)
}

func copyManifest(ctx context.Context, src, dst oci.Interface, srcRepo, dstRepo string, desc oci.Descriptor, contents []byte, tags []string, completeStatus string, progress func(jsonstream.Message)) error {
	var manifest oci.IndexOrManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		return fmt.Errorf("decode manifest %s: %w", desc.Digest, err)
	}

	children := childManifests{
		{descriptor: desc, tags: tags, completeStatus: completeStatus},
	}
	if len(manifest.Manifests) > 0 && len(tags) > 0 {
		selected, err := childManifestForRuntime(manifest.Manifests)
		if err != nil {
			return err
		}
		children = childManifestsForPull(manifest.Manifests, selected, tags)
	}

	for _, child := range children {
		if child.descriptor.Digest == desc.Digest {
			continue
		}
		childReader, err := src.GetManifest(ctx, srcRepo, child.descriptor.Digest)
		if err != nil {
			return fmt.Errorf("get child manifest %s: %w", child.descriptor.Digest, err)
		}
		childContents, err := io.ReadAll(childReader)
		closeErr := childReader.Close()
		if err != nil {
			return fmt.Errorf("read child manifest %s: %w", child.descriptor.Digest, err)
		}
		if closeErr != nil {
			return fmt.Errorf("close child manifest %s: %w", child.descriptor.Digest, closeErr)
		}
		if err := copyManifest(ctx, src, dst, srcRepo, dstRepo, childReader.Descriptor(), childContents, child.tags, child.completeStatus, progress); err != nil {
			return err
		}
	}
	if len(manifest.Manifests) > 0 {
		return nil
	}

	if manifest.Config != nil {
		if err := copyBlob(ctx, src, dst, srcRepo, dstRepo, *manifest.Config, "Download complete", nil); err != nil {
			return err
		}
	}
	for _, layer := range manifest.Layers {
		if err := copyBlob(ctx, src, dst, srcRepo, dstRepo, layer, completeStatus, progress); err != nil {
			return err
		}
	}

	_, err := dst.PushManifest(ctx, dstRepo, contents, desc.MediaType, &oci.PushManifestParameters{
		Digest: desc.Digest,
		Tags:   tags,
	})
	if err != nil {
		return fmt.Errorf("push manifest %s: %w", desc.Digest, err)
	}
	return nil
}

func copyBlob(ctx context.Context, src, dst oci.Interface, srcRepo, dstRepo string, desc oci.Descriptor, completeStatus string, progress func(jsonstream.Message)) error {
	id := shortDigest(desc.Digest.String())
	if cached, err := blobExists(ctx, dst, dstRepo, desc.Digest); err != nil {
		return err
	} else if cached {
		if progress != nil {
			progress(jsonstream.Message{Status: "Already exists", ID: id, Progress: &jsonstream.Progress{}})
		}
		return nil
	}

	if len(desc.Data) > 0 {
		_, err := dst.PushBlob(ctx, dstRepo, desc, bytes.NewReader(desc.Data))
		if err == nil && progress != nil {
			progress(jsonstream.Message{Status: completeStatus, ID: id, Progress: &jsonstream.Progress{}})
		}
		return err
	}

	blob, err := src.GetBlob(ctx, srcRepo, desc.Digest)
	if err != nil {
		return fmt.Errorf("get blob %s: %w", desc.Digest, err)
	}
	defer func() {
		if closeErr := blob.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	reader := io.Reader(blob)
	if progress != nil {
		reader = &progressReader{
			Reader: blob,
			total:  desc.Size,
			id:     id,
			report: progress,
		}
	}
	if _, err := dst.PushBlob(ctx, dstRepo, desc, reader); err != nil {
		return fmt.Errorf("push blob %s: %w", desc.Digest, err)
	}
	if progress != nil {
		progress(jsonstream.Message{Status: completeStatus, ID: id, Progress: &jsonstream.Progress{}})
	}
	return nil
}

type progressReader struct {
	io.Reader
	total    int64
	current  int64
	nextStep int64
	id       string
	report   func(jsonstream.Message)
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 {
		r.current += int64(n)
		step := max(r.total/20, 256*1024)
		if r.current >= r.nextStep || r.current == r.total {
			r.report(jsonstream.Message{
				Status:   "Downloading",
				ID:       r.id,
				Progress: &jsonstream.Progress{Current: r.current, Total: r.total},
			})
			r.nextStep = r.current + step
		}
	}
	return n, err
}

type childManifest struct {
	descriptor     oci.Descriptor
	tags           []string
	completeStatus string
}

type childManifests []childManifest

func childManifestsForPull(manifests []oci.Descriptor, selected oci.Descriptor, tags []string) childManifests {
	children := childManifests{{descriptor: selected, tags: tags, completeStatus: "Pull complete"}}
	for _, manifest := range manifests {
		if manifest.Annotations["vnd.docker.reference.type"] != "attestation-manifest" {
			continue
		}
		if manifest.Annotations["vnd.docker.reference.digest"] != selected.Digest.String() {
			continue
		}
		children = append(children, childManifest{descriptor: manifest, completeStatus: "Download complete"})
	}
	return children
}

func childManifestForRuntime(manifests []oci.Descriptor) (oci.Descriptor, error) {
	for _, manifest := range manifests {
		if manifest.Platform == nil {
			continue
		}
		if manifest.Platform.OS == runtime.GOOS && manifest.Platform.Architecture == runtime.GOARCH {
			return manifest, nil
		}
	}
	return oci.Descriptor{}, fmt.Errorf("no manifest found for platform %s/%s", runtime.GOOS, runtime.GOARCH)
}

func tagExists(ctx context.Context, registry oci.Interface, repo, tag string) (bool, error) {
	manifest, err := registry.GetTag(ctx, repo, tag)
	if err == nil {
		return true, manifest.Close()
	}
	if errors.Is(err, oci.ErrNameUnknown) || errors.Is(err, oci.ErrManifestUnknown) {
		return false, nil
	}
	return false, err
}

func blobExists(ctx context.Context, registry oci.Interface, repo string, digest oci.Digest) (bool, error) {
	blob, err := registry.GetBlob(ctx, repo, digest)
	if err == nil {
		return true, blob.Close()
	}
	if errors.Is(err, oci.ErrNameUnknown) || errors.Is(err, oci.ErrBlobUnknown) {
		return false, nil
	}
	return false, err
}

type dockerHubTokenTransport struct {
	base   http.RoundTripper
	mu     sync.Mutex
	tokens map[string]string
}

func (t *dockerHubTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "registry-1.docker.io" {
		return t.base.RoundTrip(req)
	}

	scope, err := dockerHubScope(req.URL.Path)
	if err != nil {
		return nil, err
	}

	token, err := t.bearerToken(req.Context(), scope)
	if err != nil {
		return nil, err
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(req)
}

func (t *dockerHubTokenTransport) bearerToken(ctx context.Context, scope string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if token := t.tokens[scope]; token != "" {
		return token, nil
	}

	values := url.Values{}
	values.Set("service", "registry.docker.io")
	values.Set("scope", scope)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://auth.docker.io/token?"+values.Encode(), nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("docker hub token request failed: %s", resp.Status)
	}

	var token struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return "", err
	}
	value := token.Token
	if value == "" {
		value = token.AccessToken
	}
	if value == "" {
		return "", fmt.Errorf("docker hub token response did not include a token")
	}
	t.tokens[scope] = value
	return value, nil
}

func dockerHubScope(path string) (string, error) {
	parts := strings.Split(strings.TrimPrefix(path, "/v2/"), "/")
	for i, part := range parts {
		if part == "manifests" || part == "blobs" {
			return "repository:" + strings.Join(parts[:i], "/") + ":pull", nil
		}
	}
	return "", fmt.Errorf("cannot infer Docker Hub auth scope from path %q", path)
}

func parseSourceRef(fromImage, tag string) (string, string, string, error) {
	ref, err := ociref.ParseRelative(fromImage)
	if err != nil {
		return "", "", "", err
	}
	if ref.Digest.String() != "" {
		return "", "", "", fmt.Errorf("digest references are not supported yet")
	}
	parsedTag := ref.Tag
	if parsedTag == "" {
		parsedTag = "latest"
	}
	if tag != "" {
		parsedTag = tag
	}
	if parsedTag == "" {
		return "", "", "", fmt.Errorf("tag is required")
	}
	return ref.Host, ref.Repository, parsedTag, nil
}

func internalRepo(registry, repo string) string {
	return "dink/" + registry + "/" + repo
}

func shortDigest(digest string) string {
	if len(digest) <= 19 {
		return digest
	}
	return digest[:19]
}
