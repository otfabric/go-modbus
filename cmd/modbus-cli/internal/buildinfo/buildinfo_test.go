// SPDX-License-Identifier: MIT

package buildinfo

import (
	"runtime"
	"runtime/debug"
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
		`","built":"2026-11-05T15:41:49Z","go":"` + runtime.Version() +
		`","platform":"` + runtime.GOOS + "/" + runtime.GOARCH + `"}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestJSONDev(t *testing.T) {
	withBuildInfo(t, nil)
	got := Resolve("tool", "", "", "", "").JSON()
	want := `{"name":"tool","version":"dev","tag":"","commit":"unknown","built":"unknown","go":"` +
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
