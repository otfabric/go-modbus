// SPDX-License-Identifier: MIT

package buildinfo

import (
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
)

const sha = "1802d0f9afe8642d1e278120b76fcdfac967cb9e"

func withBuildInfo(t *testing.T, bi *debug.BuildInfo) {
	t.Helper()
	old := readBuildInfo
	readBuildInfo = func() (*debug.BuildInfo, bool) { return bi, bi != nil }
	t.Cleanup(func() { readBuildInfo = old })
}

func platform() string { return runtime.Version() + ", " + runtime.GOOS + "/" + runtime.GOARCH }

func TestTextAllFields(t *testing.T) {
	withBuildInfo(t, nil)
	got := Resolve("tool", "3.5.1", "v3.5.1", sha, "2026-11-05T15:41:49Z").String()
	want := "tool 3.5.1 (tag v3.5.1, commit " + sha + ", built 2026-11-05T15:41:49Z, " + platform() + ")"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestTextLeadingVStripped(t *testing.T) {
	withBuildInfo(t, nil)
	if v := Resolve("tool", "v3.5.1", "v3.5.1", sha, "x").Version; v != "3.5.1" {
		t.Fatalf("version %q", v)
	}
}

func TestTextWithoutTag(t *testing.T) {
	withBuildInfo(t, nil)
	got := Resolve("tool", "3.5.1", "", sha, "2026-11-05T15:41:49Z").String()
	want := "tool 3.5.1 (commit " + sha + ", built 2026-11-05T15:41:49Z, " + platform() + ")"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if strings.Contains(got, "tag") {
		t.Fatalf("tag part printed: %q", got)
	}
}

func TestDevFallback(t *testing.T) {
	withBuildInfo(t, nil)
	got := Resolve("tool", "", "", "", "").String()
	want := "tool dev (commit unknown, built unknown, " + platform() + ")"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestDevFallbackFromBuildInfo(t *testing.T) {
	withBuildInfo(t, &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: sha},
			{Key: "vcs.modified", Value: "true"},
		},
	})
	got := Resolve("tool", "", "", "", "").String()
	want := "tool dev (commit " + sha + "-dirty, built unknown, " + platform() + ")"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestGoInstallVersion(t *testing.T) {
	withBuildInfo(t, &debug.BuildInfo{
		Main:     debug.Module{Version: "v3.5.1"},
		Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: sha}, {Key: "vcs.modified", Value: "false"}},
	})
	i := Resolve("tool", "", "", "", "")
	if i.Version != "3.5.1" || i.Commit != sha || i.Tag != "" {
		t.Fatalf("got %+v", i)
	}
}

func TestInjectedWinsOverBuildInfo(t *testing.T) {
	withBuildInfo(t, &debug.BuildInfo{
		Main:     debug.Module{Version: "v9.9.9"},
		Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "other"}},
	})
	i := Resolve("tool", "3.5.1", "v3.5.1", sha, "d")
	if i.Version != "3.5.1" || i.Commit != sha {
		t.Fatalf("got %+v", i)
	}
}

func TestJSONKeyOrderAndValues(t *testing.T) {
	withBuildInfo(t, nil)
	got := Resolve("tool", "3.5.1", "v3.5.1", sha, "2026-11-05T15:41:49Z").JSON()
	want := `{"name":"tool","version":"3.5.1","tag":"v3.5.1","commit":"` + sha +
		`","built":"2026-11-05T15:41:49Z","libraries":[],"go":"` + runtime.Version() +
		`","platform":"` + runtime.GOOS + "/" + runtime.GOARCH + `"}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestJSONDev(t *testing.T) {
	withBuildInfo(t, nil)
	got := Resolve("tool", "", "", "", "").JSON()
	want := `{"name":"tool","version":"dev","tag":"","commit":"unknown","built":"unknown","libraries":[],"go":"` +
		runtime.Version() + `","platform":"` + runtime.GOOS + "/" + runtime.GOARCH + `"}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestGoBuildStampsAreNotReleases(t *testing.T) {
	for _, v := range []string{"", "(devel)", "v0.0.0-20261107205103-37f27f89321d", "v0.0.0-20261107205103-37f27f89321d+dirty",
		"v1.2.4-0.20261107205103-37f27f89321d", "v1.2.3+dirty"} {
		withBuildInfo(t, &debug.BuildInfo{Main: debug.Module{Version: v}})
		if got := Resolve("tool", "", "", "", "").Version; got != "dev" {
			t.Errorf("%q: version %q, want dev", v, got)
		}
	}
}

func TestLibrariesFormatting(t *testing.T) {
	base := Info{Name: "tool", Version: "1.0.0", Tag: "v1.0.0", Commit: sha, Built: "d", Go: "go1.26.0", Platform: "linux/amd64"}
	cases := []struct {
		libs       []Library
		text, json string
	}{
		{nil, "tool 1.0.0 (tag v1.0.0, commit " + sha + ", built d, go1.26.0, linux/amd64)", `"libraries":[]`},
		{[]Library{{"go-canbus", "0.8.0"}}, "tool 1.0.0 (tag v1.0.0, commit " + sha + ", built d, go-canbus 0.8.0, go1.26.0, linux/amd64)",
			`"libraries":[{"name":"go-canbus","version":"0.8.0"}]`},
		{[]Library{{"go-canbus", "0.8.0"}, {"go-canopen", "0.5.1"}},
			"tool 1.0.0 (tag v1.0.0, commit " + sha + ", built d, go-canbus 0.8.0, go-canopen 0.5.1, go1.26.0, linux/amd64)",
			`"libraries":[{"name":"go-canbus","version":"0.8.0"},{"name":"go-canopen","version":"0.5.1"}]`},
	}
	for _, tc := range cases {
		i := base
		i.Libraries = tc.libs
		if got := i.String(); got != tc.text {
			t.Errorf("text %q\nwant %q", got, tc.text)
		}
		want := `{"name":"tool","version":"1.0.0","tag":"v1.0.0","commit":"` + sha + `","built":"d",` + tc.json + `,"go":"go1.26.0","platform":"linux/amd64"}`
		if got := i.JSON(); got != want {
			t.Errorf("json %s\nwant %s", got, want)
		}
	}
}

func TestLibrariesFromBuildInfo(t *testing.T) {
	withBuildInfo(t, &debug.BuildInfo{
		Main: debug.Module{Path: "github.com/otfabric/go-own/cmd/tool"},
		Deps: []*debug.Module{
			{Path: "github.com/otfabric/go-own/cmd/tool", Version: "v9.9.9"},
			{Path: "github.com/otfabric/go-own", Version: "v1.0.0"},
			{Path: "github.com/otfabric/go-serial", Version: "v0.2.1"},
			{Path: "github.com/otfabric/go-canbus", Version: "v0.8.0", Replace: &debug.Module{Path: "github.com/otfabric/go-canbus", Version: "v0.8.1"}},
			{Path: "github.com/otfabric/docsync", Version: "v0.3.1"},
			{Path: "github.com/spf13/cobra", Version: "v1.10.2"},
			{Path: "github.com/otfabric/go-mms", Version: "v0.0.0-20261107205103-37f27f89321d"},
		},
	})
	got := Resolve("tool", "1.0.0", "", sha, "d", "github.com/otfabric/go-own").Libraries
	want := []Library{{"go-canbus", "0.8.1"}, {"go-mms", "0.0.0-20261107205103-37f27f89321d"}, {"go-serial", "0.2.1"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
