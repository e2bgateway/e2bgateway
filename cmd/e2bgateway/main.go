// Package main is the entry point for the E2BGateway server.
package main

import (
	"fmt"
	"os"

	"github.com/e2bgateway/e2bgateway/internal/cmd"
)

var (
	version   = "dev"
	buildDate = "unknown"
)

func main() {
	rootCmd := cmd.NewRootCommand(version, buildDate)
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
