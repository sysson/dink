package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/docker/oci/ociref"
	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/types"
	registryv1 "github.com/sysson/dink/sdk/registry/v1"
	"github.com/sysson/dink/sdk/registry/v1/registryconnect"
)

// Client is dink's view of dinki's image operations.
type Client struct {
	rpc registryconnect.RegistryServiceClient
	// err is why the client could not be built; every call reports it.
	err error
}

// NewClient returns a client for the RegistryService at baseURL. httpClient
// carries the mutual TLS configuration.
func NewClient(httpClient connect.HTTPClient, baseURL string, options ...connect.ClientOption) *Client {
	return &Client{rpc: registryconnect.NewRegistryServiceClient(httpClient, baseURL, options...)}
}

// Unavailable returns a client whose calls all fail with err, so endpoints
// that do not need dinki keep working.
func Unavailable(err error) *Client {
	return &Client{err: err}
}

func (c *Client) requestIdentity(ctx context.Context) (*registryv1.Identity, error) {
	if c.err != nil {
		return nil, c.err
	}
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("missing identity in context")
	}
	return IdentityToProto(id), nil
}

// Authenticate asks dinki to verify auth against its registry.
func (c *Client) Authenticate(ctx context.Context, auth *registry.AuthConfig) (string, error) {
	if c.err != nil {
		return "", c.err
	}
	response, err := c.rpc.Login(ctx, &registryv1.LoginRequest{Auth: AuthToProto(auth)})
	if err != nil {
		return "", FromConnectError(err)
	}
	return response.GetIdentityToken(), nil
}

// PullImage asks dinki to pull ref and copies its progress messages to
// options.OutStream. If the Docker client stops reading, the pull is
// cancelled.
func (c *Client) PullImage(ctx context.Context, ref ociref.Reference, options imagebackend.PullOptions) error {
	id, err := c.requestIdentity(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := c.rpc.Pull(ctx, &registryv1.PullRequest{
		Identity:    id,
		Reference:   ReferenceToProto(ref),
		Auth:        AuthToProto(options.AuthConfig),
		MetaHeaders: HeadersToProto(options.MetaHeaders),
		Platforms:   PlatformsToProto(options.Platforms),
	})
	if err != nil {
		return FromConnectError(err)
	}
	defer func() { _ = stream.Close() }()
	for stream.Receive() {
		if options.OutStream == nil {
			continue
		}
		if _, err := options.OutStream.Write(stream.Msg().GetMessage()); err != nil {
			cancel()
			return fmt.Errorf("writing pull progress: %w", err)
		}
	}
	return FromConnectError(stream.Err())
}

// Images lists the images in the caller's namespace.
func (c *Client) Images(ctx context.Context, _ types.ImageListOptions) ([]imagetypes.Summary, error) {
	id, err := c.requestIdentity(ctx)
	if err != nil {
		return nil, err
	}
	response, err := c.rpc.ListImages(ctx, &registryv1.ListImagesRequest{Identity: id})
	if err != nil {
		return nil, FromConnectError(err)
	}
	summaries := make([]imagetypes.Summary, 0, len(response.GetImages()))
	for _, summary := range response.GetImages() {
		summaries = append(summaries, SummaryFromProto(summary))
	}
	return summaries, nil
}

// ImageDelete removes name from the caller's namespace.
func (c *Client) ImageDelete(ctx context.Context, name string, options imagebackend.RemoveOptions) ([]imagetypes.DeleteResponse, error) {
	id, err := c.requestIdentity(ctx)
	if err != nil {
		return nil, err
	}
	response, err := c.rpc.RemoveImage(ctx, &registryv1.RemoveImageRequest{
		Identity:  id,
		Name:      name,
		Platforms: PlatformsToProto(options.Platforms),
	})
	if err != nil {
		return nil, FromConnectError(err)
	}
	records := make([]imagetypes.DeleteResponse, 0, len(response.GetRecords()))
	for _, record := range response.GetRecords() {
		records = append(records, imagetypes.DeleteResponse{Untagged: record.GetUntagged(), Deleted: record.GetDeleted()})
	}
	return records, nil
}

// TagImage assigns ref to the image identified by name in the caller's namespace.
func (c *Client) TagImage(ctx context.Context, name string, ref ociref.Reference) error {
	id, err := c.requestIdentity(ctx)
	if err != nil {
		return err
	}
	_, err = c.rpc.TagImage(ctx, &registryv1.TagImageRequest{
		Identity: id,
		Name:     name,
		Target:   ReferenceToProto(ref),
	})
	return FromConnectError(err)
}

// ImageInspect describes name in the caller's namespace.
func (c *Client) ImageInspect(ctx context.Context, name string, options imagebackend.ImageInspectOpts) (*imagebackend.InspectData, error) {
	id, err := c.requestIdentity(ctx)
	if err != nil {
		return nil, err
	}
	response, err := c.rpc.InspectImage(ctx, &registryv1.InspectImageRequest{
		Identity:  id,
		Name:      name,
		Platform:  OptionalPlatformToProto(options.Platform),
		Manifests: options.Manifests,
	})
	if err != nil {
		return nil, FromConnectError(err)
	}
	data := &imagebackend.InspectData{}
	if err := json.Unmarshal(response.GetImage(), data); err != nil {
		return nil, fmt.Errorf("decoding image inspect response: %w", err)
	}
	return data, nil
}

// ImageHistory returns the layers name was built from, newest first.
func (c *Client) ImageHistory(ctx context.Context, name string, platform *ocispec.Platform) ([]imagetypes.HistoryResponseItem, error) {
	id, err := c.requestIdentity(ctx)
	if err != nil {
		return nil, err
	}
	response, err := c.rpc.ImageHistory(ctx, &registryv1.ImageHistoryRequest{
		Identity: id,
		Name:     name,
		Platform: OptionalPlatformToProto(platform),
	})
	if err != nil {
		return nil, FromConnectError(err)
	}
	var history []imagetypes.HistoryResponseItem
	if err := json.Unmarshal(response.GetHistory(), &history); err != nil {
		return nil, fmt.Errorf("decoding image history response: %w", err)
	}
	return history, nil
}

// ImageAttestations returns the in-toto statements attached to name.
func (c *Client) ImageAttestations(ctx context.Context, name string, options imagebackend.AttestationOpts) ([]imagetypes.AttestationStatement, error) {
	id, err := c.requestIdentity(ctx)
	if err != nil {
		return nil, err
	}
	response, err := c.rpc.ImageAttestations(ctx, &registryv1.ImageAttestationsRequest{
		Identity:         id,
		Name:             name,
		Platform:         OptionalPlatformToProto(options.Platform),
		PredicateTypes:   options.PredicateTypes,
		IncludeStatement: options.IncludeStatement,
	})
	if err != nil {
		return nil, FromConnectError(err)
	}
	var statements []imagetypes.AttestationStatement
	if err := json.Unmarshal(response.GetStatements(), &statements); err != nil {
		return nil, fmt.Errorf("decoding image attestations response: %w", err)
	}
	return statements, nil
}

// Query executes a GraphQL document against dinki metadata and returns the
// raw GraphQL response ({"data": ..., "errors": ...}).
func (c *Client) Query(ctx context.Context, document, operationName string, variables map[string]any) (json.RawMessage, error) {
	if c.err != nil {
		return nil, c.err
	}
	if document == "" {
		return nil, errors.New("query document is required")
	}
	var encoded []byte
	if variables != nil {
		var err error
		if encoded, err = json.Marshal(variables); err != nil {
			return nil, err
		}
	}
	response, err := c.rpc.Query(ctx, &registryv1.QueryRequest{Query: document, OperationName: operationName, Variables: encoded})
	if err != nil {
		return nil, FromConnectError(err)
	}
	return response.GetResponse(), nil
}

// IssuePullCredential creates, or replaces, the caller's namespace pull
// credential and returns its username and password.
func (c *Client) IssuePullCredential(ctx context.Context) (string, string, error) {
	id, err := c.requestIdentity(ctx)
	if err != nil {
		return "", "", err
	}
	response, err := c.rpc.IssuePullCredential(ctx, &registryv1.IssuePullCredentialRequest{Identity: id})
	if err != nil {
		return "", "", FromConnectError(err)
	}
	return response.GetUsername(), response.GetPassword(), nil
}

// RevokePullCredential removes the caller's namespace pull credential.
func (c *Client) RevokePullCredential(ctx context.Context) error {
	id, err := c.requestIdentity(ctx)
	if err != nil {
		return err
	}
	_, err = c.rpc.RevokePullCredential(ctx, &registryv1.RevokePullCredentialRequest{Identity: id})
	return FromConnectError(err)
}
