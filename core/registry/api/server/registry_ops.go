package server

import (
	"context"
	"encoding/json"
	"errors"

	"connectrpc.com/connect"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/sysson/dink/core/registry/api"
	registryv1 "github.com/sysson/dink/core/registry/api/v1"
	"github.com/sysson/dink/pkg/filters"
)

type pushStreamWriter struct {
	stream *connect.ServerStream[registryv1.PushResponse]
}

func (w pushStreamWriter) Write(p []byte) (int, error) {
	if err := w.stream.Send(&registryv1.PushResponse{Message: append([]byte(nil), p...)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *Server) Push(ctx context.Context, request *registryv1.PushRequest, stream *connect.ServerStream[registryv1.PushResponse]) error {
	ctx, err := withIdentity(ctx, request.GetIdentity())
	if err != nil {
		return err
	}
	ref, err := api.ReferenceFromProto(request.GetReference())
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return api.ToConnectError(s.images.PushImage(ctx, ref, imagebackend.PushOptions{
		AuthConfig: api.AuthFromProto(request.GetAuth()), MetaHeaders: api.HeadersFromProto(request.GetMetaHeaders()),
		Platforms: api.PlatformsFromProto(request.GetPlatforms()), OutStream: pushStreamWriter{stream: stream},
	}))
}

func (s *Server) Search(ctx context.Context, request *registryv1.SearchRequest) (*registryv1.SearchResponse, error) {
	ctx, err := withIdentity(ctx, request.GetIdentity())
	if err != nil {
		return nil, err
	}
	if request.GetLimit() < 0 || request.GetLimit() > 100 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("search limit must be between 0 and 100"))
	}
	searchFilters, err := filters.FromJSON(request.GetFilters())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	results, err := s.images.Search(ctx, searchFilters, request.GetTerm(), int(request.GetLimit()),
		api.AuthFromProto(request.GetAuth()), api.HeadersFromProto(request.GetMetaHeaders()))
	if err != nil {
		return nil, api.ToConnectError(err)
	}
	data, err := json.Marshal(results)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &registryv1.SearchResponse{Results: data}, nil
}
