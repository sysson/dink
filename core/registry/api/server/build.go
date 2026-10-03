package server

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/docker/oci/ociref"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/registry"
	"github.com/sysson/dink/core/registry/api"
	registryv1 "github.com/sysson/dink/core/registry/api/v1"
)

type buildCredentials interface {
	IssueBuild(context.Context, string, []string) (string, string, error)
	RevokeBuild(context.Context, string, string) error
}

func (s *Server) IssueBuildCredential(ctx context.Context, request *registryv1.IssueBuildCredentialRequest) (*registryv1.IssueBuildCredentialResponse, error) {
	namespace, err := credentialNamespace(request.GetIdentity())
	if err != nil {
		return nil, err
	}
	credentials, ok := s.credentials.(buildCredentials)
	if !ok {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("build credentials are unavailable"))
	}
	if len(request.GetNames()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("at least one image tag is required"))
	}
	var references, repositories []string
	for _, name := range request.GetNames() {
		ref, err := ociref.ParseRelative(name)
		if err != nil || ref.Digest != "" || ref.Tag == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("build output must be a tagged image reference"))
		}
		mapped := registry.BuildReference(identity.Identity{Namespace: namespace}, ref)
		if !ociref.IsValidRepository(mapped.Repository) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("mapped build repository is invalid"))
		}
		references = append(references, mapped.String())
		repositories = append(repositories, mapped.Repository)
	}
	username, password, err := credentials.IssueBuild(ctx, namespace, repositories)
	if err != nil {
		return nil, api.ToConnectError(err)
	}
	return &registryv1.IssueBuildCredentialResponse{Username: username, Password: password, References: references}, nil
}

func (s *Server) RevokeBuildCredential(ctx context.Context, request *registryv1.RevokeBuildCredentialRequest) (*registryv1.RevokeBuildCredentialResponse, error) {
	namespace, err := credentialNamespace(request.GetIdentity())
	if err != nil {
		return nil, err
	}
	credentials, ok := s.credentials.(buildCredentials)
	if !ok {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("build credentials are unavailable"))
	}
	if err := credentials.RevokeBuild(ctx, namespace, request.GetUsername()); err != nil {
		return nil, api.ToConnectError(err)
	}
	return &registryv1.RevokeBuildCredentialResponse{}, nil
}
