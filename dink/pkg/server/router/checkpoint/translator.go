package checkpoint

type Translator interface {
	CheckpointCreate()
	CheckpointDelete()
	CheckpointList()
}
