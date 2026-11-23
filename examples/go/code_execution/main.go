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

// Backend: mock, or a backend with Python execution.
// Run: go run ./examples/go/code_execution
// Expected: A greeting and 5050 (or the mock execution response); --stream uses the gateway WebSocket.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"

	"github.com/e2bgateway/e2bgateway/examples/go/internal/example"
	"github.com/e2bgateway/e2bgateway/pkg/client"
)

const source = "print('Hello from code execution!', sum(range(1, 101)))"

func main() {
	stream := flag.Bool("stream", false, "execute via the gateway's WebSocket")
	flag.Parse()
	example.Main(func(ctx context.Context, c *client.Client) error {
		return example.WithSandbox(ctx, c, func(id string) error {
			if *stream {
				return runStream(ctx, id)
			}
			return example.RunPython(ctx, c, id, source)
		})
	})
}

func runStream(ctx context.Context, id string) (err error) {
	address, key, err := example.Endpoint()
	if err != nil {
		return err
	}
	u, err := url.Parse(address + "/sandboxes/" + url.PathEscape(id) + "/ws")
	if err != nil {
		return err
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	headers := make(http.Header)
	if key != "" {
		headers.Set("X-API-Key", key)
	}
	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, u.String(), headers)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return err
	}
	defer conn.Close()

	// DialContext only covers the handshake. Close the socket when the caller
	// cancels or the streaming timeout expires to unblock pending I/O.
	streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stop := context.AfterFunc(streamCtx, func() { _ = conn.Close() })
	defer stop()
	defer func() {
		if err != nil && streamCtx.Err() != nil {
			err = streamCtx.Err()
		}
	}()

	if err := conn.WriteJSON(map[string]any{
		"type": "code/exec", "data": client.CodeExecRequest{Code: source, Language: "python"},
	}); err != nil {
		return err
	}
	for {
		var frame struct {
			Type string `json:"type"`
			Data struct {
				Content  string `json:"content"`
				Message  string `json:"message"`
				ExitCode int    `json:"exitCode"`
			} `json:"data"`
		}
		if err := conn.ReadJSON(&frame); err != nil {
			return err
		}
		switch frame.Type {
		case "stdout", "stderr":
			fmt.Print(frame.Data.Content)
		case "error":
			return fmt.Errorf("stream error: %s", frame.Data.Message)
		case "result":
			if frame.Data.ExitCode != 0 {
				return fmt.Errorf("execution exited with %d", frame.Data.ExitCode)
			}
			return nil
		}
	}
}
