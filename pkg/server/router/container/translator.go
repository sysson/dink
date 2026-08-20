package container

type execTranslator interface {
	ContainerExecCreate()
	ContainerExecInspect()
	ContainerExecResize()
	ContainerExecStart()
	ExecExists()
}

type copyTranslator interface {
	ContainerArchivePath()
	ContainerExport()
	ContainerExtractToDir()
	ContainerStatPath()
}

type stateTranslator interface {
	ContainerCreate()
	ContainerKill()
	ContainerPause()
	ContainerRename()
	ContainerResize()
	ContainerRestart()
	ContainerRm()
	ContainerStart()
	ContainerStop()
	ContainerUnpause()
	ContainerUpdate()
	ContainerWait()
}

type monitorTranslator interface {
	ContainerChanges()
	ContainerInspect()
	ContainerLogs()
	ContainerStats()
	ContainerTop()
	Containers()
}

type attachTranslator interface {
	ContainerAttach()
}

type systemTranslator interface {
	ContainerPrune()
}

type commitTranslator interface {
	CreateImageFromContainer()
}

type sysInfoTranslator interface {
	RawSysInfo()
}

type Translator interface {
	commitTranslator
	execTranslator
	copyTranslator
	stateTranslator
	monitorTranslator
	attachTranslator
	systemTranslator
	sysInfoTranslator
}
