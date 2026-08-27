package build

type Translator interface {
	Build()
	PruneCache()
	Cancel()
}
