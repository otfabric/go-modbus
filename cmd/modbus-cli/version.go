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

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print build version information",
	Args:  cobra.NoArgs,
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Printf("modbus-cli %s\n", version)
		fmt.Printf("  tag:        %s\n", tag)
		fmt.Printf("  commit:     %s\n", commit)
		fmt.Printf("  built:      %s\n", buildDate)
	},
}
