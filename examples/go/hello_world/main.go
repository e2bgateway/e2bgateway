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

// Backend: mock, or a backend with command support.
// Run: go run ./examples/go/hello_world
// Expected: A greeting, sandbox ID, and cleanup.
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/e2bgateway/e2bgateway/examples/go/internal/example"
	"github.com/e2bgateway/e2bgateway/pkg/client"
)

func main() {
	example.Main(func(ctx context.Context, c *client.Client) error {
		return example.WithSandbox(ctx, c, func(id string) error {
			result, err := c.RunCommand(ctx, id, client.CommandRequest{Command: "echo 'Hello from E2BGateway!'"})
			if err != nil {
				return err
			}
			if result.ExitCode != 0 || !strings.Contains(result.Stdout, "Hello from E2BGateway!") {
				return fmt.Errorf("unexpected command result (exit %d): %s %s", result.ExitCode, result.Stdout, result.Stderr)
			}
			fmt.Printf("Sandbox ID: %s\nOutput: %s", id, result.Stdout)
			return nil
		})
	})
}
