// Package api holds the wire conversions for dinki's internal
// RegistryService and the client dink uses to call it. dink makes no OCI
// requests itself; every image operation goes through this client.
package api

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ociref"
	imagetypes "github.com/moby/moby/api/types/image"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/types"
	registryv1 "github.com/sysson/dink/sdk/registry/v1"
	"github.com/sysson/syskit/httpx"
)

func IdentityToProto(id identity.Identity) *registryv1.Identity {
	return &registryv1.Identity{
		Namespace:    id.Namespace,
		Organization: id.Organization,
		CommonName:   id.CommonName,
		Anonymous:    id.Anonymous,
	}
}

func IdentityFromProto(id *registryv1.Identity) identity.Identity {
	return identity.Identity{
		Namespace:    id.GetNamespace(),
		Organization: id.GetOrganization(),
		CommonName:   id.GetCommonName(),
		Anonymous:    id.GetAnonymous(),
	}
}

func AuthToProto(auth *types.RegistryAuth) *registryv1.RegistryAuth {
	if auth == nil {
		return nil
	}
	return &registryv1.RegistryAuth{
		Username:      auth.Username,
		Password:      auth.Password,
		IdentityToken: auth.RefreshToken,
		RegistryToken: auth.AccessToken,
		ServerAddress: auth.ServerAddress,
	}
}

func AuthFromProto(auth *registryv1.RegistryAuth) *types.RegistryAuth {
	if auth == nil {
		return nil
	}
	return &types.RegistryAuth{
		Username:      auth.GetUsername(),
		Password:      auth.GetPassword(),
		RefreshToken:  auth.GetIdentityToken(),
		AccessToken:   auth.GetRegistryToken(),
		ServerAddress: auth.GetServerAddress(),
	}
}

func ReferenceToProto(ref ociref.Reference) *registryv1.Reference {
	return &registryv1.Reference{Host: ref.Host, Repository: ref.Repository, Tag: ref.Tag, Digest: string(ref.Digest)}
}

func ReferenceFromProto(ref *registryv1.Reference) (ociref.Reference, error) {
	result := ociref.Reference{Host: ref.GetHost(), Repository: ref.GetRepository(), Tag: ref.GetTag()}
	if ref.GetDigest() != "" {
		digest, err := ocidigest.Parse(ref.GetDigest())
		if err != nil {
			return ociref.Reference{}, err
		}
		result.Digest = digest
	}
	return result, nil
}

func PlatformToProto(platform ocispec.Platform) *registryv1.Platform {
	return &registryv1.Platform{
		Os:           platform.OS,
		Architecture: platform.Architecture,
		Variant:      platform.Variant,
		OsVersion:    platform.OSVersion,
		OsFeatures:   platform.OSFeatures,
	}
}

func PlatformFromProto(platform *registryv1.Platform) ocispec.Platform {
	return ocispec.Platform{
		OS:           platform.GetOs(),
		Architecture: platform.GetArchitecture(),
		Variant:      platform.GetVariant(),
		OSVersion:    platform.GetOsVersion(),
		OSFeatures:   platform.GetOsFeatures(),
	}
}

// OptionalPlatformToProto keeps "no platform requested" distinct from a
// zero platform, which selects the daemon's default.
func OptionalPlatformToProto(platform *ocispec.Platform) *registryv1.Platform {
	if platform == nil {
		return nil
	}
	return PlatformToProto(*platform)
}

// OptionalPlatformFromProto is the inverse of OptionalPlatformToProto.
func OptionalPlatformFromProto(platform *registryv1.Platform) *ocispec.Platform {
	if platform == nil {
		return nil
	}
	result := PlatformFromProto(platform)
	return &result
}

func PlatformsToProto(platforms []ocispec.Platform) []*registryv1.Platform {
	result := make([]*registryv1.Platform, len(platforms))
	for i, platform := range platforms {
		result[i] = PlatformToProto(platform)
	}
	return result
}

func PlatformsFromProto(platforms []*registryv1.Platform) []ocispec.Platform {
	if len(platforms) == 0 {
		return nil
	}
	result := make([]ocispec.Platform, len(platforms))
	for i, platform := range platforms {
		result[i] = PlatformFromProto(platform)
	}
	return result
}

func HeadersToProto(headers map[string][]string) map[string]*registryv1.HeaderValues {
	if len(headers) == 0 {
		return nil
	}
	result := make(map[string]*registryv1.HeaderValues, len(headers))
	for key, values := range headers {
		result[key] = &registryv1.HeaderValues{Values: values}
	}
	return result
}

func HeadersFromProto(headers map[string]*registryv1.HeaderValues) map[string][]string {
	if len(headers) == 0 {
		return nil
	}
	result := make(map[string][]string, len(headers))
	for key, values := range headers {
		result[key] = values.GetValues()
	}
	return result
}

func SummaryToProto(summary imagetypes.Summary) *registryv1.ImageSummary {
	result := &registryv1.ImageSummary{
		Id:          summary.ID,
		RepoTags:    summary.RepoTags,
		RepoDigests: summary.RepoDigests,
		Created:     summary.Created,
		Size:        summary.Size,
	}
	if descriptor := summary.Descriptor; descriptor != nil {
		result.Target = &registryv1.Descriptor{
			MediaType: descriptor.MediaType,
			Digest:    descriptor.Digest.String(),
			Size:      descriptor.Size,
		}
		if descriptor.Platform != nil {
			result.Target.Platform = PlatformToProto(*descriptor.Platform)
		}
	}
	return result
}

func SummaryFromProto(summary *registryv1.ImageSummary) imagetypes.Summary {
	result := imagetypes.Summary{
		ID:          summary.GetId(),
		RepoTags:    summary.GetRepoTags(),
		RepoDigests: summary.GetRepoDigests(),
		Created:     summary.GetCreated(),
		Size:        summary.GetSize(),
	}
	if descriptor := summary.GetTarget(); descriptor != nil {
		result.Descriptor = &ocispec.Descriptor{
			MediaType: descriptor.GetMediaType(),
			Digest:    digest.Digest(descriptor.GetDigest()),
			Size:      descriptor.GetSize(),
		}
		if descriptor.GetPlatform() != nil {
			platform := PlatformFromProto(descriptor.GetPlatform())
			result.Descriptor.Platform = &platform
		}
	}
	return result
}

// ToConnectError maps a service error to a Connect error so the client can
// restore the HTTP status dink reports to Docker clients.
func ToConnectError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*connect.Error](err); ok {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err)
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	}
	if httpErr, ok := errors.AsType[*httpx.HTTPError](err); ok {
		code := connect.CodeUnknown
		switch httpErr.StatusCode {
		case http.StatusBadRequest:
			code = connect.CodeInvalidArgument
		case http.StatusUnauthorized:
			code = connect.CodeUnauthenticated
		case http.StatusForbidden:
			code = connect.CodePermissionDenied
		case http.StatusNotFound:
			code = connect.CodeNotFound
		case http.StatusConflict:
			code = connect.CodeAlreadyExists
		case http.StatusRequestTimeout:
			code = connect.CodeDeadlineExceeded
		case http.StatusGone:
			code = connect.CodeFailedPrecondition
		}
		return connect.NewError(code, httpErr.Message)
	}
	return connect.NewError(connect.CodeUnknown, err)
}

// FromConnectError is the inverse of ToConnectError.
func FromConnectError(err error) error {
	if err == nil {
		return nil
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		return err
	}
	message := errors.New(connectErr.Message())
	switch connectErr.Code() {
	case connect.CodeInvalidArgument:
		return httpx.BadRequest(message)
	case connect.CodeUnauthenticated:
		return httpx.Unauthorized(message)
	case connect.CodePermissionDenied:
		return httpx.Forbidden(message)
	case connect.CodeNotFound:
		return httpx.NotFound(message)
	case connect.CodeAlreadyExists:
		return httpx.Conflict(message)
	case connect.CodeFailedPrecondition:
		return httpx.Gone(message)
	case connect.CodeCanceled:
		return errors.Join(context.Canceled, message)
	case connect.CodeDeadlineExceeded:
		return errors.Join(context.DeadlineExceeded, message)
	case connect.CodeUnavailable:
		return httpx.NewHTTPError(http.StatusServiceUnavailable, message)
	}
	return message
}
