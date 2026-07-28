package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newVersionCommand(version, buildDate string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("E2BGateway %s (built %s)\n", version, buildDate)
		},
	}
}
