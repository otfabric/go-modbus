// SPDX-License-Identifier: MIT

package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/otfabric/go-modbus/cmd/modbus-cli/internal/buildinfo"
)

// Set via -ldflags at build time (Makefile, .github/workflows/release.yml);
// empty when not injected, and then resolved by buildinfo:
//
//	-X main.version=${VERSION}
//	-X main.tag=${TAG}
//	-X main.commit=${COMMIT}
//	-X main.buildDate=${BUILD_DATE}
var (
	version   = ""
	tag       = ""
	commit    = ""
	buildDate = ""
)

// build is the resolved build information of this binary.
func build() buildinfo.Info {
	return buildinfo.Resolve("modbus-cli", version, tag, commit, buildDate)
}

func newVersionCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version, tag, commit, build date, Go version and platform",
		Long: `Print the version, tag, commit, build date, Go version and platform on one
line, or with --json as one line of JSON:

  {"name":"modbus-cli","version":"…","tag":"…","commit":"…","built":"…","go":"…","platform":"…"}`,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.NoArgs(cmd, args); err != nil {
				return usageError(err)
			}
			return nil
		},
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, _ []string) error {
			b := build()
			line := b.String()
			if opts.json {
				line = b.JSON()
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), line); err != nil {
				return outputError(err)
			}
			return nil
		},
	}
}
