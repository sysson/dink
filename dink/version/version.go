package version

import (
	"runtime/debug"
	"sync"
)

var (
	v    info
	once sync.Once
)

type info struct {
	Version       string
	Commit        string
	Date          string
	APIVersion    string
	MinAPIVersion string
}

func SetAPI(apiVersion, minAPIVersion string) {
	v.APIVersion = apiVersion
	v.MinAPIVersion = minAPIVersion
}

func IsDev() bool {
	return v.Version == "Dev"
}

func Get() info {
	once.Do(func() {
		bs, ok := debug.ReadBuildInfo()
		v = info{
			Version: "Dev",
			Commit:  "None",
			Date:    "Unknown",
		}
		if ok {
			if bs.Main.Version != "(devel)" {
				v.Version = bs.Main.Version
			}
			for _, setting := range bs.Settings {
				switch setting.Key {
				case "vcs.revision":
					v.Commit = setting.Value
				case "vcs.time":
					v.Date = setting.Value
				}
			}
		}
	})
	return v
}
