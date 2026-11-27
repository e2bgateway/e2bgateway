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

// Backend: agent-sandbox/OpenSandbox with Python and a reachable sandbox port URL.
// Run: go run ./examples/go/cloud_browser
// Expected: An HTTP 200 response from a page served by the sandbox.
package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e2bgateway/e2bgateway/examples/go/internal/example"
	"github.com/e2bgateway/e2bgateway/pkg/client"
)

func main() {
	example.Main(func(ctx context.Context, c *client.Client) error {
		return example.WithSandbox(ctx, c, func(id string) error {
			// Serve only the example directory, not the sandbox filesystem.
			result, err := c.RunCommand(ctx, id, client.CommandRequest{Command: `
mkdir -p /tmp/egw-browser && printf '%s' '<h1>Hello from E2BGateway</h1>' > /tmp/egw-browser/index.html &&
(python3 -m http.server 3000 --directory /tmp/egw-browser >/tmp/egw-browser.log 2>&1 </dev/null &)
`})
			if err != nil {
				return err
			}
			if result.ExitCode != 0 {
				return fmt.Errorf("could not start sandbox web server: %s", result.Stderr)
			}
			address, err := c.GetPortURL(ctx, id, 3000)
			if err != nil {
				return err
			}
			return checkPage(ctx, address)
		})
	})
}

func checkPage(ctx context.Context, address string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if !strings.Contains(address, "://") {
		address = "https://" + address
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	// The sandbox endpoint is not the gateway API; do not forward the API key.
	hc := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if resp, err := hc.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				fmt.Println("Sandbox HTTP page: 200 OK")
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("sandbox port is not reachable: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
