// Copyright The E2BGateway Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	v1 "github.com/e2bgateway/e2bgateway/internal/api/v1"
)

// NewRootCommand creates the root cobra command.
func NewRootCommand(version, commit, buildDate string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "e2bgateway",
		Short: "E2B-compatible API Gateway for AI Agent Sandboxes",
		Long: `E2BGateway acts as an abstraction gateway layer for AI agent sandboxes.
It provides a fully compatible interface aligned with the official E2B client
protocol, transparently routing requests to diverse underlying agent runtime
implementations such as agent-sandbox and OpenSandbox.`,
		Version: version,
	}
	cmd.SetVersionTemplate(fmt.Sprintf("E2BGateway %s (commit %s, built %s)\n", version, commit, buildDate))

	cmd.PersistentFlags().String("config", "", "Path to configuration file")
	cmd.PersistentFlags().String("log-level", "info", "Log level (debug, info, warn, error)")
	cmd.PersistentFlags().String("log-format", "json", "Log format (json, text)")

	cmd.AddCommand(newServeCommand(v1.BuildInfo{Version: version, Commit: commit, BuildDate: buildDate}))
	cmd.AddCommand(newVersionCommand(version, commit, buildDate))

	return cmd
}
