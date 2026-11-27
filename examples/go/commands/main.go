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

// Backend: mock, or agent-sandbox/OpenSandbox with a command runtime.
// Run: go run ./examples/go/commands
// Expected: A greeting and the working directory (or a mock command response).
package main

import (
	"context"
	"fmt"

	"github.com/e2bgateway/e2bgateway/examples/go/internal/example"
	"github.com/e2bgateway/e2bgateway/pkg/client"
)

func main() {
	example.Main(func(ctx context.Context, c *client.Client) error {
		return example.WithSandbox(ctx, c, func(id string) error {
			// Set the working directory in the command so the example also
			// works with backends that do not support the optional cwd field.
			result, err := c.RunCommand(ctx, id, client.CommandRequest{
				Command: `cd /tmp && printf '%s\n' 'Hello from a command' && pwd`,
			})
			if err != nil {
				return err
			}
			if result.ExitCode != 0 {
				return fmt.Errorf("command failed: %s", result.Stderr)
			}
			fmt.Print(result.Stdout)
			return nil
		})
	})
}
