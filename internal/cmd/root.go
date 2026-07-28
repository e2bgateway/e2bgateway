// Package cmd implements the CLI commands for E2BGateway.
package cmd

import (
	"github.com/spf13/cobra"
)

// NewRootCommand creates the root cobra command.
func NewRootCommand(version, buildDate string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "e2bgateway",
		Short: "E2B-compatible API Gateway for AI Agent Sandboxes",
		Long: `E2BGateway acts as an abstraction gateway layer for AI agent sandboxes.
It provides a fully compatible interface aligned with the official E2B client
protocol, transparently routing requests to diverse underlying agent runtime
implementations such as agent-sandbox and OpenSandbox.`,
		Version: version,
	}

	cmd.PersistentFlags().String("config", "", "Path to configuration file")
	cmd.PersistentFlags().String("log-level", "info", "Log level (debug, info, warn, error)")
	cmd.PersistentFlags().String("log-format", "json", "Log format (json, text)")

	cmd.AddCommand(newServeCommand())
	cmd.AddCommand(newVersionCommand(version, buildDate))

	return cmd
}
