package image

import (
	"context"

	"github.com/docker/oci/ociref"
	"github.com/sysson/dink/core/types"
	imagetypes "github.com/moby/moby/api/types/image"
)

type Translator interface {
	imageTranslator
	importExportTranslator
	registryTranslator
}

type imageTranslator interface {
	ImageDelete()
	ImageHistory()
	Images(ctx context.Context, options types.ImageListOptions) ([]imagetypes.Summary, error)
	GetImage()
	ImageInspect()
	ImageAttestations()
	TagImage()
	ImagePrune()
}

type importExportTranslator interface {
	LoadImage()
	ImportImage()
	ExportImage()
}

type registryTranslator interface {
	PullImage(ctx context.Context, ref ociref.Reference, options types.ImagePullOptions) error
	PushImage()
}

type Searcher interface {
	Search()
}
