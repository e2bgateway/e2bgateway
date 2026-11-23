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

// Backend: agent-sandbox or OpenSandbox with a Python sandbox and file/command support.
// Run: go run ./examples/go/coding_agent
// Expected: All tests passed!, followed by sandbox cleanup.
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
			const project = "/tmp/egw-calculator"
			if err := c.MakeDir(ctx, id, project); err != nil {
				return err
			}
			if err := c.WriteFile(ctx, id, project+"/calculator.py", "def add(a, b): return a + b\ndef multiply(a, b): return a * b\n"); err != nil {
				return err
			}
			if err := c.WriteFile(ctx, id, project+"/test_calc.py", `from calculator import add, multiply
assert add(2, 3) == 5
assert multiply(3, 4) == 12
print("All tests passed!")
`); err != nil {
				return err
			}
			result, err := c.RunCommand(ctx, id, client.CommandRequest{Command: "python3 /tmp/egw-calculator/test_calc.py"})
			if err != nil {
				return err
			}
			if result.ExitCode != 0 || !strings.Contains(result.Stdout, "All tests passed!") {
				return fmt.Errorf("unexpected test result (exit %d): %s %s", result.ExitCode, result.Stdout, result.Stderr)
			}
			fmt.Print(result.Stdout)
			fmt.Println("Done.")
			return nil
		})
	})
}
