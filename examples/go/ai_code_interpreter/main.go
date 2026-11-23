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

// Backend: agent-sandbox/OpenSandbox with Python and file operations.
// Run: go run ./examples/go/ai_code_interpreter
// Expected: A JSON report containing the computed mean (20).
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
			// Store state in a file; each REST execution may use a fresh process.
			if err := c.WriteFile(ctx, id, "/tmp/inputs.json", "[10, 20, 30]"); err != nil {
				return err
			}
			if err := example.RunPython(ctx, c, id, `
import json
from pathlib import Path
from statistics import mean

values = json.loads(Path("/tmp/inputs.json").read_text())
Path("/tmp/report.json").write_text(json.dumps({"mean": mean(values)}))
`); err != nil {
				return err
			}
			report, err := c.ReadFile(ctx, id, "/tmp/report.json")
			if err != nil {
				return err
			}
			fmt.Println(report)
			return nil
		})
	})
}
