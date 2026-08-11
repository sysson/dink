package container

type stateTranslator interface {
	ContainerCreate() error
}

type Translator interface {
	stateTranslator
}
