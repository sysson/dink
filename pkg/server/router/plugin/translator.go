package plugin

type Translator interface {
	DisablePlugin()
	EnablePlugin()
	ListPlugins()
	InspectPlugin()
	RemovePlugin()
	SetPlugin()
	PluginPrivileges()
	PullPlugin()
	PushPlugin()
	UpgradePlugin()
	CreatePluginFromContext()
}
