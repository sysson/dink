// Package server implements dinki's internal RegistryService. It runs every
// image operation dink requests: pulls fetch from upstream registries and
// write straight into dinki's storage, and listing and removal resolve
// through the GraphQL metadata layer.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/registry"
	"github.com/sysson/dink/core/registry/api"
	"github.com/sysson/dink/core/types"
	registryv1 "github.com/sysson/dink/sdk/registry/v1"
	"github.com/sysson/dink/sdk/registry/v1/registryconnect"
)

// Server implements registryconnect.RegistryServiceHandler.
type Server struct {
	images  *registry.RegistryService
	queries registry.Querier
}

var _ registryconnect.RegistryServiceHandler = (*Server)(nil)

func New(images *registry.RegistryService, queries registry.Querier) (*Server, error) {
	if images == nil || queries == nil {
		return nil, errors.New("registry API requires an image service and a query service")
	}
	return &Server{images: images, queries: queries}, nil
}

// Handler returns the path to mount the service on and its handler.
func (s *Server) Handler(options ...connect.HandlerOption) (string, http.Handler) {
	return registryconnect.NewRegistryServiceHandler(s, options...)
}

func withIdentity(ctx context.Context, id *registryv1.Identity) (context.Context, error) {
	if id == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("identity is required"))
	}
	return identity.NewContext(ctx, api.IdentityFromProto(id)), nil
}

func (s *Server) Login(ctx context.Context, request *registryv1.LoginRequest) (*registryv1.LoginResponse, error) {
	auth := api.AuthFromProto(request.GetAuth())
	if auth == nil {
		auth = &types.RegistryAuth{}
	}
	token, err := registry.Authenticate(ctx, *auth)
	if err != nil {
		return nil, api.ToConnectError(err)
	}
	return &registryv1.LoginResponse{IdentityToken: token}, nil
}

// streamWriter sends each progress message written by the pull as one
// stream message.
type streamWriter struct {
	stream *connect.ServerStream[registryv1.PullResponse]
}

func (w streamWriter) Write(p []byte) (int, error) {
	message := make([]byte, len(p))
	copy(message, p)
	if err := w.stream.Send(&registryv1.PullResponse{Message: message}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *Server) Pull(ctx context.Context, request *registryv1.PullRequest, stream *connect.ServerStream[registryv1.PullResponse]) error {
	ctx, err := withIdentity(ctx, request.GetIdentity())
	if err != nil {
		return err
	}
	ref, err := api.ReferenceFromProto(request.GetReference())
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	err = s.images.PullImage(ctx, ref, types.ImagePullOptions{
		Auth:        api.AuthFromProto(request.GetAuth()),
		MetaHeaders: api.HeadersFromProto(request.GetMetaHeaders()),
		OutStream:   streamWriter{stream: stream},
		Platforms:   api.PlatformsFromProto(request.GetPlatforms()),
	})
	return api.ToConnectError(err)
}

func (s *Server) ListImages(ctx context.Context, request *registryv1.ListImagesRequest) (*registryv1.ListImagesResponse, error) {
	ctx, err := withIdentity(ctx, request.GetIdentity())
	if err != nil {
		return nil, err
	}
	summaries, err := s.images.Images(ctx, types.ImageListOptions{})
	if err != nil {
		return nil, api.ToConnectError(err)
	}
	response := &registryv1.ListImagesResponse{Images: make([]*registryv1.ImageSummary, 0, len(summaries))}
	for _, summary := range summaries {
		response.Images = append(response.Images, api.SummaryToProto(summary))
	}
	return response, nil
}

func (s *Server) RemoveImage(ctx context.Context, request *registryv1.RemoveImageRequest) (*registryv1.RemoveImageResponse, error) {
	ctx, err := withIdentity(ctx, request.GetIdentity())
	if err != nil {
		return nil, err
	}
	records, err := s.images.ImageDelete(ctx, request.GetName(), imagebackend.RemoveOptions{
		Platforms: api.PlatformsFromProto(request.GetPlatforms()),
	})
	if err != nil {
		return nil, api.ToConnectError(err)
	}
	response := &registryv1.RemoveImageResponse{Records: make([]*registryv1.RemoveImageRecord, 0, len(records))}
	for _, record := range records {
		response.Records = append(response.Records, &registryv1.RemoveImageRecord{Untagged: record.Untagged, Deleted: record.Deleted})
	}
	return response, nil
}

func (s *Server) Query(ctx context.Context, request *registryv1.QueryRequest) (*registryv1.QueryResponse, error) {
	var variables map[string]any
	if len(request.GetVariables()) > 0 {
		if err := json.Unmarshal(request.GetVariables(), &variables); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("variables must be a JSON object"))
		}
	}
	result, err := json.Marshal(s.queries.Exec(ctx, request.GetQuery(), request.GetOperationName(), variables))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &registryv1.QueryResponse{Response: result}, nil
}
