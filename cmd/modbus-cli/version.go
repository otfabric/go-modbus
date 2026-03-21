package main

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

//go:embed version.txt
var embeddedVersion string

// version is set via -ldflags "-X main.version=..." at build time.
// When empty, the embedded version.txt is used as fallback.
var version string

func getVersion() string {
	if version != "" {
		return version
	}
	return strings.TrimSpace(embeddedVersion)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number",
	Args:  cobra.NoArgs,
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Printf("modbus-cli v%s\n", getVersion())
	},
}
