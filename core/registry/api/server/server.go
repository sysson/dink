// Package server implements dinki's internal RegistryService. It runs every
// image operation dink requests: pulls fetch from upstream registries and
// write straight into dinki's storage, and listing and removal resolve
// through the store's metadata index.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"github.com/99designs/gqlgen/graphql"
	registrytypes "github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/registry"
	"github.com/sysson/dink/core/registry/api"
	registryv1 "github.com/sysson/dink/core/registry/api/v1"
	"github.com/sysson/dink/core/registry/api/v1/registryconnect"
	"github.com/sysson/dink/core/registry/pullauth"
	"github.com/sysson/dink/core/types"
)

// Querier executes GraphQL documents against the registry metadata. It is
// satisfied by [github.com/sysson/ocistore/query.Service].
type Querier interface {
	Exec(ctx context.Context, document, operationName string, variables map[string]any) *graphql.Response
}

// Credentials issues and revokes namespace pull credentials. It is satisfied
// by [github.com/sysson/dink/core/registry/pullauth.Store].
type Credentials interface {
	Issue(ctx context.Context, namespace string) (string, error)
	Revoke(ctx context.Context, namespace string) error
	Verify(ctx context.Context, namespace, password string) (bool, error)
}

// Server implements registryconnect.RegistryServiceHandler.
type Server struct {
	images      *registry.RegistryService
	queries     Querier
	credentials Credentials
}

var _ registryconnect.RegistryServiceHandler = (*Server)(nil)

func New(images *registry.RegistryService, queries Querier, credentials Credentials) (*Server, error) {
	if images == nil || queries == nil || credentials == nil {
		return nil, errors.New("registry API requires an image service, a query service and a credential store")
	}
	return &Server{images: images, queries: queries, credentials: credentials}, nil
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

func credentialNamespace(id *registryv1.Identity) (string, error) {
	if id == nil || !pullauth.ValidNamespace(id.GetNamespace()) {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("identity with a valid namespace is required"))
	}
	return id.GetNamespace(), nil
}

func (s *Server) IssuePullCredential(ctx context.Context, request *registryv1.IssuePullCredentialRequest) (*registryv1.IssuePullCredentialResponse, error) {
	namespace, err := credentialNamespace(request.GetIdentity())
	if err != nil {
		return nil, err
	}
	if password := request.GetExistingPassword(); password != "" {
		valid, err := s.credentials.Verify(ctx, namespace, password)
		if err != nil {
			return nil, api.ToConnectError(err)
		}
		if valid {
			return &registryv1.IssuePullCredentialResponse{Username: namespace, Password: password}, nil
		}
	}
	password, err := s.credentials.Issue(ctx, namespace)
	if err != nil {
		return nil, api.ToConnectError(err)
	}
	return &registryv1.IssuePullCredentialResponse{Username: namespace, Password: password}, nil
}

func (s *Server) RevokePullCredential(ctx context.Context, request *registryv1.RevokePullCredentialRequest) (*registryv1.RevokePullCredentialResponse, error) {
	namespace, err := credentialNamespace(request.GetIdentity())
	if err != nil {
		return nil, err
	}
	if err := s.credentials.Revoke(ctx, namespace); err != nil {
		return nil, api.ToConnectError(err)
	}
	return &registryv1.RevokePullCredentialResponse{}, nil
}

func (s *Server) Login(ctx context.Context, request *registryv1.LoginRequest) (*registryv1.LoginResponse, error) {
	auth := api.AuthFromProto(request.GetAuth())
	if auth == nil {
		auth = &registrytypes.AuthConfig{}
	}
	token, err := registry.Authenticate(ctx, auth)
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
	err = s.images.PullImage(ctx, ref, imagebackend.PullOptions{
		AuthConfig:  api.AuthFromProto(request.GetAuth()),
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
	summaries, err := s.images.Images(ctx, types.ImageListOptions{Manifests: request.GetManifests()})
	if err != nil {
		return nil, api.ToConnectError(err)
	}
	response := &registryv1.ListImagesResponse{Images: make([]*registryv1.ImageSummary, 0, len(summaries))}
	for _, summary := range summaries {
		image, err := api.SummaryToProto(summary)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		response.Images = append(response.Images, image)
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

func (s *Server) TagImage(ctx context.Context, request *registryv1.TagImageRequest) (*registryv1.TagImageResponse, error) {
	ctx, err := withIdentity(ctx, request.GetIdentity())
	if err != nil {
		return nil, err
	}
	if request.GetTarget() == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("tag target is required"))
	}
	target, err := api.ReferenceFromProto(request.GetTarget())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := s.images.TagImage(ctx, request.GetName(), target); err != nil {
		return nil, api.ToConnectError(err)
	}
	return &registryv1.TagImageResponse{}, nil
}

func (s *Server) InspectImage(ctx context.Context, request *registryv1.InspectImageRequest) (*registryv1.InspectImageResponse, error) {
	ctx, err := withIdentity(ctx, request.GetIdentity())
	if err != nil {
		return nil, err
	}
	data, err := s.images.ImageInspect(ctx, request.GetName(), imagebackend.ImageInspectOpts{
		Manifests: request.GetManifests(),
		Platform:  api.OptionalPlatformFromProto(request.GetPlatform()),
	})
	if err != nil {
		return nil, api.ToConnectError(err)
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &registryv1.InspectImageResponse{Image: encoded}, nil
}

func (s *Server) ImageHistory(ctx context.Context, request *registryv1.ImageHistoryRequest) (*registryv1.ImageHistoryResponse, error) {
	ctx, err := withIdentity(ctx, request.GetIdentity())
	if err != nil {
		return nil, err
	}
	history, err := s.images.ImageHistory(ctx, request.GetName(), api.OptionalPlatformFromProto(request.GetPlatform()))
	if err != nil {
		return nil, api.ToConnectError(err)
	}
	encoded, err := json.Marshal(history)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &registryv1.ImageHistoryResponse{History: encoded}, nil
}

func (s *Server) ImageAttestations(ctx context.Context, request *registryv1.ImageAttestationsRequest) (*registryv1.ImageAttestationsResponse, error) {
	ctx, err := withIdentity(ctx, request.GetIdentity())
	if err != nil {
		return nil, err
	}
	statements, err := s.images.ImageAttestations(ctx, request.GetName(), imagebackend.AttestationOpts{
		Platform:         api.OptionalPlatformFromProto(request.GetPlatform()),
		PredicateTypes:   request.GetPredicateTypes(),
		IncludeStatement: request.GetIncludeStatement(),
	})
	if err != nil {
		return nil, api.ToConnectError(err)
	}
	encoded, err := json.Marshal(statements)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &registryv1.ImageAttestationsResponse{Statements: encoded}, nil
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
