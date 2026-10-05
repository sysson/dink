package version

import (
	"runtime/debug"
	"testing"
)

func TestBuildInfo(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		build                             *debug.BuildInfo
		release, commit, date             string
		wantVersion, wantCommit, wantDate string
		dirty                             bool
	}{
		{name: "missing metadata", wantVersion: "Dev", wantCommit: "None", wantDate: "Unknown"},
		{name: "empty module", build: &debug.BuildInfo{}, wantVersion: "Dev", wantCommit: "None", wantDate: "Unknown"},
		{name: "module version", build: &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, wantVersion: "v1.2.3", wantCommit: "None", wantDate: "Unknown"},
		{
			name: "development VCS", build: &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "git-commit"},
					{Key: "vcs.time", Value: "git-time"},
					{Key: "vcs.modified", Value: "true"},
				},
			},
			wantVersion: "Dev", wantCommit: "git-commit", wantDate: "git-time", dirty: true,
		},
		{name: "injected without metadata", release: "v2.0.0", commit: "release-commit", date: "build-time", wantVersion: "v2.0.0", wantCommit: "release-commit", wantDate: "build-time"},
		{
			name: "injected overrides VCS", build: &debug.BuildInfo{
				Main:     debug.Module{Version: "v1.0.0"},
				Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "old"}, {Key: "vcs.time", Value: "old"}},
			},
			release: "v2.0.0", commit: "new", date: "new-time",
			wantVersion: "v2.0.0", wantCommit: "new", wantDate: "new-time",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := buildInfo(tc.build, tc.release, tc.commit, tc.date)
			if got.Version != tc.wantVersion || got.Commit != tc.wantCommit || got.Date != tc.wantDate || got.Dirty != tc.dirty {
				t.Fatalf("buildInfo = %+v", got)
			}
		})
	}
}
