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

// Backend: mock, or a backend with sandbox file operations.
// Run: go run ./examples/go/filesystem
// Expected: A directory listing and the uploaded text read back.
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
			const dir = "/tmp/egw-files"
			const path = dir + "/hello & world.txt"
			const text = "Hello from E2BGateway!"
			if err := c.MakeDir(ctx, id, dir); err != nil {
				return err
			}
			if err := c.WriteFile(ctx, id, path, text); err != nil {
				return err
			}
			got, err := c.ReadFile(ctx, id, path)
			if err != nil {
				return err
			}
			if got != text {
				return fmt.Errorf("unexpected file content: %q", got)
			}
			entries, err := c.ListFiles(ctx, id, dir)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				fmt.Printf("%s (%s)\n", entry.Name, entry.Type)
			}
			fmt.Printf("Read back: %s\n", got)
			return c.RemoveFile(ctx, id, path)
		})
	})
}
