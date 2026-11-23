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

// Backend: mock, or any backend exposing sandbox details and text files.
// Run: go run ./examples/go/connect_sandbox
// Expected: The saved contents retrieved by ID without creating a second sandbox.
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
			const path = "/tmp/state.txt"
			const state = "persistent state"
			if err := c.WriteFile(ctx, id, path, state); err != nil {
				return err
			}

			// Keep the sandbox running; killing it would destroy the state.
			address, key, err := example.Endpoint()
			if err != nil {
				return err
			}
			fresh := example.NewClient(address, key)
			info, err := fresh.GetSandbox(ctx, id)
			if err != nil {
				return err
			}
			got, err := fresh.ReadFile(ctx, info.SandboxID, path)
			if err != nil {
				return err
			}
			if got != state {
				return fmt.Errorf("unexpected state: %q", got)
			}
			fmt.Printf("Reconnected to %s: %s\n", info.SandboxID, got)
			return nil
		})
	})
}
