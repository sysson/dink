package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/docker/oci"
	"github.com/docker/oci/ociref"
	registrytypes "github.com/moby/moby/api/types/registry"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/syskit/httpx"
)

func InspectDistribution(ctx context.Context, name string, auth *registrytypes.AuthConfig) (registrytypes.DistributionInspect, error) {
	ref, err := ociref.ParseRelative(name)
	if err != nil || ref.Repository == "" {
		return registrytypes.DistributionInspect{}, httpx.BadRequest(fmt.Errorf("invalid image reference %q", name))
	}
	if ref.Tag == "" && ref.Digest == "" {
		ref.Tag = "latest"
	}
	client, err := NewClient(ref.Host, ClientOptions{Auth: auth, Transport: RegistryTransport(nil, nil)})
	if err != nil {
		return registrytypes.DistributionInspect{}, err
	}
	result, err := inspectDistribution(ctx, client, ref)
	if errors.Is(err, oci.ErrUnauthorized) {
		return result, httpx.Unauthorized(err)
	}
	if errors.Is(err, oci.ErrDenied) {
		return result, httpx.Forbidden(err)
	}
	if isNotFound(err) {
		return result, httpx.NotFound(err)
	}
	return result, err
}

func inspectDistribution(ctx context.Context, client oci.Interface, ref ociref.Reference) (registrytypes.DistributionInspect, error) {
	if ref.Digest == "" {
		descriptor, err := client.ResolveTag(ctx, ref.Repository, ref.Tag)
		if err != nil {
			return registrytypes.DistributionInspect{}, err
		}
		ref.Digest = descriptor.Digest
	}
	descriptor, err := getManifest(ctx, client, ref)
	if err != nil {
		return registrytypes.DistributionInspect{}, err
	}
	result := registrytypes.DistributionInspect{Descriptor: pushOCIDescriptor(descriptor)}
	manifest, err := readBlob[oci.IndexOrManifest](bytes.NewReader(descriptor.Data))
	if err != nil {
		return result, err
	}
	switch {
	case isIndex(descriptor.MediaType):
		for _, child := range manifest.Manifests {
			if child.Platform != nil {
				result.Platforms = append(result.Platforms, ociPlatform(*platformOf(child.Platform)))
			}
		}
	case isManifest(descriptor.MediaType):
		if manifest.Config == nil {
			return result, errors.New("distribution manifest has no image config")
		}
		blob, err := client.GetBlob(ctx, ref.Repository, manifest.Config.Digest)
		if err != nil {
			return result, err
		}
		defer func() { _ = blob.Close() }()
		config, err := readBlob[ocispec.Image](blob)
		if err != nil {
			return result, err
		}
		result.Platforms = []ocispec.Platform{config.Platform}
	default:
		return result, fmt.Errorf("unsupported distribution media type %q", descriptor.MediaType)
	}
	return result, nil
}
