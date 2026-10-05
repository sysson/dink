package version

import (
	"fmt"
	"net/http"
	"runtime"
	"runtime/debug"
	"sync"

	"github.com/moby/moby/client/pkg/versions"
	"github.com/sysson/dink/core/server/middleware"
	"github.com/sysson/dink/core/types"
)

var (
	ReleaseVersion string
	BuildCommit    string
	BuildDate      string
	v              info
	once           sync.Once
	V              = Get()
)

type APIVersion struct{}

type info struct {
	Version       string
	Commit        string
	Date          string
	Dirty         bool
	APIVersion    string
	MinAPIVersion string
}

func IsDev() bool {
	return v.Version == "Dev"
}

func Get() info {
	once.Do(func() {
		bs, ok := debug.ReadBuildInfo()
		if !ok {
			bs = nil
		}
		v = buildInfo(bs, ReleaseVersion, BuildCommit, BuildDate)
	})
	return v
}

func buildInfo(bs *debug.BuildInfo, release, commit, date string) info {
	result := info{
		Version: "Dev", Commit: "None", Date: "Unknown",
		MinAPIVersion: types.MinAPIVersion, APIVersion: types.APIVersion,
	}
	if bs != nil {
		if bs.Main.Version != "" && bs.Main.Version != "(devel)" {
			result.Version = bs.Main.Version
		}
		for _, setting := range bs.Settings {
			switch setting.Key {
			case "vcs.revision":
				result.Commit = setting.Value
			case "vcs.time":
				result.Date = setting.Value
			case "vcs.modified":
				result.Dirty = setting.Value == "true"
			}
		}
	}
	if release != "" {
		result.Version = release
	}
	if commit != "" {
		result.Commit = commit
	}
	if date != "" {
		result.Date = date
	}
	return result
}

func Middleware(serverVersion, defaultAPIVersion, minAPIVersion string) middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Server", fmt.Sprintf("Docker/%s (%s)", serverVersion, runtime.GOOS))
			w.Header().Set("Api-Version", defaultAPIVersion)
			w.Header().Set("Ostype", runtime.GOOS)
			apiVersion := r.PathValue("version")
			if apiVersion == "" {
				apiVersion = defaultAPIVersion
			} else {
				next = http.StripPrefix("/v"+apiVersion, next)
			}
			if versions.LessThan(apiVersion, minAPIVersion) {
				http.Error(w, fmt.Sprintf("API version %s is not supported. Minimum supported version is %s", apiVersion, minAPIVersion), http.StatusBadRequest)
				return
			}
			if versions.GreaterThan(apiVersion, defaultAPIVersion) {
				http.Error(w, fmt.Sprintf("API version %s is not supported. Maximum supported version is %s", apiVersion, defaultAPIVersion), http.StatusBadRequest)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func VersionFromRequest(r *http.Request) string {
	v := r.PathValue("version")
	if v == "" {
		return Get().APIVersion
	}
	return v
}
