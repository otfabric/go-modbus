// SPDX-License-Identifier: MIT

// docsync manifest v1 (template from github.com/otfabric/docsync; keep it as
// it is apart from the package clause and the line marked docsync:root).

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestDocsyncManifest writes the command tree to $DOCSYNC_MANIFEST for
// docsync, which checks it against the documents. Without the variable the
// test does nothing.
func TestDocsyncManifest(t *testing.T) {
	out := os.Getenv("DOCSYNC_MANIFEST")
	if out == "" {
		t.Skip("DOCSYNC_MANIFEST is not set")
	}
	root := newRootCmd() // docsync:root
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	type flag struct {
		Name      string `json:"name"`
		Shorthand string `json:"shorthand,omitempty"`
		Bool      bool   `json:"bool,omitempty"`
		Required  bool   `json:"required,omitempty"`
		Hidden    bool   `json:"hidden,omitempty"`
		Local     bool   `json:"local,omitempty"`
	}
	type command struct {
		Path        string   `json:"path"`
		Aliases     []string `json:"aliases,omitempty"`
		Hidden      bool     `json:"hidden,omitempty"`
		Runnable    bool     `json:"runnable"`
		Subcommands []string `json:"subcommands,omitempty"`
		Flags       []flag   `json:"flags,omitempty"`
	}
	var cmds []command
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		c.InitDefaultHelpFlag()
		c.InitDefaultVersionFlag()
		path := strings.TrimSpace(strings.TrimPrefix(c.CommandPath(), root.Name()))
		cmd := command{Path: path, Aliases: c.Aliases, Hidden: c.Hidden, Runnable: c.Runnable()}
		local := map[string]bool{}
		c.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) { local[f.Name] = true })
		c.PersistentFlags().VisitAll(func(f *pflag.Flag) { local[f.Name] = true })
		add := func(f *pflag.Flag) {
			req := false
			if v, ok := f.Annotations[cobra.BashCompOneRequiredFlag]; ok && len(v) > 0 && v[0] == "true" {
				req = true
			}
			cmd.Flags = append(cmd.Flags, flag{Name: f.Name, Shorthand: f.Shorthand, Bool: f.NoOptDefVal != "",
				Required: req, Hidden: f.Hidden, Local: local[f.Name]})
		}
		c.LocalFlags().VisitAll(add)
		c.InheritedFlags().VisitAll(add)
		for _, s := range c.Commands() {
			cmd.Subcommands = append(cmd.Subcommands, s.Name())
		}
		cmds = append(cmds, cmd)
		for _, s := range c.Commands() {
			walk(s)
		}
	}
	walk(root)
	data, err := json.Marshal(map[string]any{"version": 1, "name": root.Name(), "commands": cmds})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
