package image

import (
	"context"

	"github.com/moby/moby/api/types/jsonstream"
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
	PullImage(ctx context.Context, fromImage, tag string, progress func(jsonstream.Message)) (string, error)
	PushImage()
}

type Searcher interface {
	Search()
}
