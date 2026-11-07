// SPDX-License-Identifier: MIT

package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Set via -ldflags at build time:
//
//	-X main.version=${VERSION}
//	-X main.tag=${TAG}
//	-X main.commit=${COMMIT}
//	-X main.buildDate=${BUILD_DATE}
var (
	version   = "dev"
	tag       = "none"
	commit    = "unknown"
	buildDate = "unknown"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build version information",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.NoArgs(cmd, args); err != nil {
				return usageError(err)
			}
			return nil
		},
		Run: func(cmd *cobra.Command, _ []string) {
			w := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(w, "modbus-cli %s\n", version)
			_, _ = fmt.Fprintf(w, "  tag:        %s\n", tag)
			_, _ = fmt.Fprintf(w, "  commit:     %s\n", commit)
			_, _ = fmt.Fprintf(w, "  built:      %s\n", buildDate)
		},
	}
}
