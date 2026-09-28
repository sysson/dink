package image

import (
	"context"
	"io"

	"github.com/docker/oci/ociref"
	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/types"
	"github.com/sysson/dink/pkg/filters"
)

type Translator interface {
	imageTranslator
	importExportTranslator
	registryTranslator
}

type imageTranslator interface {
	ImageDelete(ctx context.Context, name string, options imagebackend.RemoveOptions) ([]imagetypes.DeleteResponse, error)
	ImageHistory(ctx context.Context, name string, platform *ocispec.Platform) ([]imagetypes.HistoryResponseItem, error)
	Images(ctx context.Context, options types.ImageListOptions) ([]imagetypes.Summary, error)
	GetImage(ctx context.Context, name string, options imagebackend.GetImageOpts) (*imagebackend.InspectData, error)
	ImageInspect(ctx context.Context, name string, options imagebackend.ImageInspectOpts) (*imagebackend.InspectData, error)
	ImageAttestations(ctx context.Context, name string, options imagebackend.AttestationOpts) ([]imagetypes.AttestationStatement, error)
	TagImage(ctx context.Context, imageID string, ref ociref.Reference) error
	ImagePrune(ctx context.Context, filters filters.Args) (*imagetypes.PruneReport, error)
}

type importExportTranslator interface {
	LoadImage(context.Context, io.ReadCloser, []ocispec.Platform, io.Writer, bool) error
	ImportImage(context.Context, ociref.Reference, *ocispec.Platform, string, io.Reader, []string) (string, error)
	ExportImage(context.Context, []string, []ocispec.Platform, io.Writer) error
}

type registryTranslator interface {
	PullImage(ctx context.Context, ref ociref.Reference, options imagebackend.PullOptions) error
	PushImage(ctx context.Context, ref ociref.Reference, options imagebackend.PushOptions) error
}

type Searcher interface {
	Search(context.Context, filters.Args, string, int, *registry.AuthConfig, map[string][]string) ([]registry.SearchResult, error)
}
