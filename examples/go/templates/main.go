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

// Backend: mock, or any backend exposing template listing and details.
// Run: go run ./examples/go/templates
// Expected: Available template IDs and the first template's build ID (if present).
package main

import (
	"context"
	"fmt"

	"github.com/e2bgateway/e2bgateway/examples/go/internal/example"
	"github.com/e2bgateway/e2bgateway/pkg/client"
)

func main() {
	example.Main(func(ctx context.Context, c *client.Client) error {
		templates, err := c.ListTemplates(ctx)
		if err != nil {
			return err
		}
		if len(templates) == 0 {
			fmt.Println("No templates available.")
			return nil
		}
		for _, tpl := range templates {
			fmt.Println(tpl.TemplateID)
		}
		first, err := c.GetTemplate(ctx, templates[0].TemplateID)
		if err != nil {
			return err
		}
		fmt.Printf("Template: %s; build: %s\n", first.TemplateID, first.BuildID)
		return nil
	})
}
