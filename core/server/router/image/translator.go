package image

import (
	"context"

	"github.com/docker/oci/ociref"
	"github.com/sysson/dink/core/types"
)

type Translator interface {
	imageTranslator
	importExportTranslator
	registryTranslator
}

type imageTranslator interface {
	ImageDelete()
	ImageHistory()
	Images()
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
	PullImage(ctx context.Context, ref ociref.Reference, options types.PullOptions) error
	PushImage()
}

type Searcher interface {
	Search()
}
