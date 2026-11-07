// SPDX-License-Identifier: MIT

// Package buildinfo resolves the version of the binary and formats it for
// the version command: one text line, or one line of JSON.
//
// Values injected at build time with -ldflags -X win. A value that was not
// injected comes from runtime/debug.ReadBuildInfo when it can (the module
// version of go install ...@vX.Y.Z, the VCS revision of go build), and is
// otherwise "dev" (version) or "unknown" (commit, build date).
package buildinfo

import (
	"encoding/json"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
)

// Info is what the version command prints. The JSON field order is part of
// the output contract.
type Info struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Tag      string `json:"tag"`
	Commit   string `json:"commit"`
	Built    string `json:"built"`
	Go       string `json:"go"`
	Platform string `json:"platform"`
}

// readBuildInfo is replaced in tests.
var readBuildInfo = debug.ReadBuildInfo

// Resolve returns the build information of the program called name, from the
// values injected at build time (empty when not injected).
func Resolve(name, version, tag, commit, buildDate string) Info {
	i := Info{
		Name:     name,
		Version:  strings.TrimPrefix(version, "v"),
		Tag:      tag,
		Commit:   commit,
		Built:    buildDate,
		Go:       runtime.Version(),
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}
	if bi, ok := readBuildInfo(); ok && bi != nil {
		if i.Version == "" && released(bi.Main.Version) {
			i.Version = strings.TrimPrefix(bi.Main.Version, "v")
		}
		if i.Commit == "" {
			var rev string
			var dirty bool
			for _, s := range bi.Settings {
				switch s.Key {
				case "vcs.revision":
					rev = s.Value
				case "vcs.modified":
					dirty = s.Value == "true"
				}
			}
			if rev != "" {
				i.Commit = rev
				if dirty {
					i.Commit += "-dirty"
				}
			}
		}
	}
	if i.Version == "" {
		i.Version = "dev"
	}
	if i.Commit == "" {
		i.Commit = "unknown"
	}
	if i.Built == "" {
		i.Built = "unknown"
	}
	return i
}

// pseudo matches the end of a Go pseudo-version (v0.0.0-20261107205103-37f27f89321d),
// which go build stamps from the VCS since Go 1.24.
var pseudo = regexp.MustCompile(`\d{14}-[0-9a-f]{12}$`)

// released reports whether a module version from the build information is a
// released version: not empty, not (devel), not a pseudo-version and not a
// build of a modified tree (+dirty).
func released(v string) bool {
	return v != "" && v != "(devel)" && !strings.HasSuffix(v, "+dirty") && !pseudo.MatchString(v)
}

// String is the text line, without a newline:
//
//	name 1.2.3 (tag v1.2.3, commit <sha>, built <date>, go1.26.0, linux/amd64)
//
// The tag part is left out when no tag was injected.
func (i Info) String() string {
	s := i.Name + " " + i.Version + " ("
	if i.Tag != "" {
		s += "tag " + i.Tag + ", "
	}
	return s + "commit " + i.Commit + ", built " + i.Built + ", " + i.Go + ", " + i.Platform + ")"
}

// JSON is the one-line JSON form, without a newline.
func (i Info) JSON() string {
	b, _ := json.Marshal(i) // cannot fail: every field is a string
	return string(b)
}
