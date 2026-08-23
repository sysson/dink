package image

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
	PullImage()
	PushImage()
}

type Searcher interface {
	Search()
}
