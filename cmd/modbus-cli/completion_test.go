// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// complete runs the hidden __complete command and returns the candidates
// (without descriptions) and the directive line.
func complete(t *testing.T, args ...string) ([]string, string) {
	t.Helper()
	stdout, _ := expectRun(t, ExitOK, append([]string{"__complete"}, args...)...)
	var got []string
	directive := ""
	for _, l := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.HasPrefix(l, ":") {
			directive = l
			continue
		}
		got = append(got, strings.SplitN(l, "\t", 2)[0])
	}
	return got, directive
}

func TestCompletionShells(t *testing.T) {
	got, _ := complete(t, "completion", "")
	for _, sh := range []string{"bash", "zsh", "fish", "powershell"} {
		if !slices.Contains(got, sh) {
			t.Errorf("completion lacks %s: %v", sh, got)
		}
		if stdout, _ := expectRun(t, ExitOK, "completion", sh); stdout == "" {
			t.Errorf("completion %s: empty script", sh)
		}
	}
}

func TestCompletionFlagValues(t *testing.T) {
	cases := []struct {
		args      []string
		want      []string
		directive string
	}{
		{[]string{"--parity", ""}, []string{"none", "even", "odd"}, ":4"},
		{[]string{"--endianness", ""}, []string{"big", "little"}, ":4"},
		{[]string{"--word-order", ""}, []string{"highfirst", "hf", "lowfirst", "lf"}, ":4"},
		{[]string{"--data-bits", ""}, []string{"0", "5", "6", "7", "8"}, ":4"},
		{[]string{"--stop-bits", ""}, []string{"0", "1", "2"}, ":4"},
		{[]string{"--target", ""}, []string{"tcp://", "tcp+tls://", "udp://", "rtu://", "rtuovertcp://", "rtuoverudp://", "ascii://", "asciiovertcp://"}, ":6"},
		{[]string{"version", "--parity", ""}, []string{"none", "even", "odd"}, ":4"},
		{[]string{"--cert", ""}, []string{"pem", "crt", "cer"}, ":8"},
		{[]string{"--key", ""}, []string{"pem", "key"}, ":8"},
		{[]string{"--timeout", ""}, nil, ":4"},
		{[]string{"--unit-id", ""}, nil, ":4"},
	}
	for _, tc := range cases {
		got, dir := complete(t, tc.args...)
		if !slices.Equal(got, tc.want) || dir != tc.directive {
			t.Errorf("%v: got %v %s, want %v %s", tc.args, got, dir, tc.want, tc.directive)
		}
	}
}

// TestEveryFlagCompletes: every flag that takes a value has a completion
// function (its value set, files of its type, or no file names).
func TestEveryFlagCompletes(t *testing.T) {
	root := newRootCmd()
	var check func(c *cobra.Command)
	check = func(c *cobra.Command) {
		visit := func(f *pflag.Flag) {
			if f.Value.Type() == "bool" {
				return
			}
			if _, ok := c.GetFlagCompletionFunc(f.Name); !ok {
				t.Errorf("%s --%s (%s) has no completion", c.CommandPath(), f.Name, f.Usage)
			}
		}
		c.LocalFlags().VisitAll(visit)
		for _, s := range c.Commands() {
			check(s)
		}
	}
	check(root)
}

func TestVersionOutput(t *testing.T) {
	plat := runtime.Version() + ", " + runtime.GOOS + "/" + runtime.GOARCH
	stdout, _ := expectRun(t, ExitOK, "version")
	if want := "modbus-cli dev (commit unknown, built unknown, " + libsText() + plat + ")\n"; stdout != want {
		t.Errorf("version: %q, want %q", stdout, want)
	}
	want := `{"name":"modbus-cli","version":"dev","tag":"","commit":"unknown","built":"unknown",` + libsJSON() + `"go":"` +
		runtime.Version() + `","platform":"` + runtime.GOOS + "/" + runtime.GOARCH + "\"}\n"
	if stdout, _ := expectRun(t, ExitOK, "version", "--json"); stdout != want {
		t.Errorf("version --json: %q, want %q", stdout, want)
	}
	if stdout, _ := expectRun(t, ExitOK, "--json", "version"); stdout != want {
		t.Errorf("--json version: %q, want %q", stdout, want)
	}

	old := [4]string{version, tag, commit, buildDate}
	t.Cleanup(func() { version, tag, commit, buildDate = old[0], old[1], old[2], old[3] })
	version, tag, commit, buildDate = "1.13.0", "v1.13.0", "1802d0f9afe8642d1e278120b76fcdfac967cb9e", "2026-11-05T15:41:49Z"
	stdout, _ = expectRun(t, ExitOK, "version")
	if want := "modbus-cli 1.13.0 (tag v1.13.0, commit 1802d0f9afe8642d1e278120b76fcdfac967cb9e, built 2026-11-05T15:41:49Z, " + libsText() + plat + ")\n"; stdout != want {
		t.Errorf("injected: %q, want %q", stdout, want)
	}
}

// libsText and libsJSON are the libraries part of the version output of
// this test binary (whatever OT Fabric modules it links).
func libsText() string {
	s := ""
	for _, l := range build().Libraries {
		s += l.Name + " " + l.Version + ", "
	}
	return s
}

func libsJSON() string {
	b, _ := json.Marshal(build().Libraries)
	return `"libraries":` + string(b) + `,`
}
