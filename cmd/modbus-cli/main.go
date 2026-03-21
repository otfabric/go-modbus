package main

import (
	"errors"
	"fmt"
	"os"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		if !errors.Is(err, errOperationFailed) {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		os.Exit(1)
	}
}
