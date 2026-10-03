package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/docker/oci/ociref"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	registryv1 "github.com/sysson/dink/core/registry/api/v1"
	"github.com/sysson/dink/pkg/filters"
)

func (c *Client) PushImage(ctx context.Context, ref ociref.Reference, options imagebackend.PushOptions) error {
	id, err := c.requestIdentity(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := c.rpc.Push(ctx, &registryv1.PushRequest{
		Identity: id, Reference: ReferenceToProto(ref), Auth: AuthToProto(options.AuthConfig),
		MetaHeaders: HeadersToProto(options.MetaHeaders), Platforms: PlatformsToProto(options.Platforms),
	})
	if err != nil {
		return FromConnectError(err)
	}
	defer func() { _ = stream.Close() }()
	for stream.Receive() {
		if options.OutStream != nil {
			if _, err := options.OutStream.Write(stream.Msg().GetMessage()); err != nil {
				cancel()
				return fmt.Errorf("writing push progress: %w", err)
			}
		}
	}
	return FromConnectError(stream.Err())
}

func (c *Client) Search(ctx context.Context, searchFilters filters.Args, term string, limit int, auth *registry.AuthConfig, headers map[string][]string) ([]registry.SearchResult, error) {
	id, err := c.requestIdentity(ctx)
	if err != nil {
		return nil, err
	}
	encodedFilters, err := filters.ToJSON(searchFilters)
	if err != nil {
		return nil, err
	}
	response, err := c.rpc.Search(ctx, &registryv1.SearchRequest{
		Identity: id, Term: term, Limit: int64(limit), Filters: encodedFilters,
		Auth: AuthToProto(auth), MetaHeaders: HeadersToProto(headers),
	})
	if err != nil {
		return nil, FromConnectError(err)
	}
	var results []registry.SearchResult
	if err := json.Unmarshal(response.GetResults(), &results); err != nil {
		return nil, fmt.Errorf("decoding search results: %w", err)
	}
	return results, nil
}
