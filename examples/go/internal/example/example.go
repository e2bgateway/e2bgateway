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

// Package example contains the shared setup and cleanup for the Go examples.
package example

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/e2bgateway/e2bgateway/pkg/client"
)

func Endpoint() (string, string, error) {
	address := os.Getenv("E2B_API_URL")
	if address == "" {
		address = os.Getenv("E2B_DOMAIN")
	}
	if address == "" {
		address = "localhost:8080"
	}
	if !strings.Contains(address, "://") {
		parsed, err := url.Parse("https://" + address)
		if err != nil {
			return "", "", err
		}
		if ip := net.ParseIP(parsed.Hostname()); parsed.Hostname() == "localhost" || ip.IsLoopback() {
			parsed.Scheme = "http"
		}
		address = parsed.String()
	}
	u, err := url.Parse(address)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" {
		return "", "", errors.New("set E2B_API_URL or E2B_DOMAIN to a gateway HTTP(S) URL")
	}
	key := os.Getenv("E2B_API_KEY")
	if key == "" {
		key = "test-key"
	}
	return strings.TrimRight(u.String(), "/"), key, nil
}

func Main(run func(context.Context, *client.Client) error) {
	address, key, err := Endpoint()
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		err = run(ctx, NewClient(address, key))
		cancel()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// NewClient creates a bounded client that does not forward gateway keys on redirects.
func NewClient(address, key string) *client.Client {
	return client.New(address, client.WithAPIKey(key), client.WithHTTPClient(&http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}))
}

func WithSandbox(ctx context.Context, c *client.Client, run func(string) error) (err error) {
	sb, err := c.CreateSandbox(ctx, client.CreateSandboxRequest{TemplateID: "base", Timeout: 120})
	if err != nil {
		return err
	}
	if sb.SandboxID == "" {
		return errors.New("missing sandbox ID")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if killErr := c.KillSandbox(cleanup, sb.SandboxID); killErr != nil {
			err = errors.Join(err, killErr)
		} else {
			fmt.Println("Killed.")
		}
	}()
	return run(sb.SandboxID)
}

func RunPython(ctx context.Context, c *client.Client, id, source string) error {
	result, err := c.ExecuteCode(ctx, id, client.CodeExecRequest{Language: "python", Code: source})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 || result.Error != "" {
		return fmt.Errorf("execution failed (exit %d): %s %s", result.ExitCode, result.Error, result.Stderr)
	}
	fmt.Print(result.Stdout)
	return nil
}

func PythonMain(source string) {
	Main(func(ctx context.Context, c *client.Client) error {
		return WithSandbox(ctx, c, func(id string) error { return RunPython(ctx, c, id, source) })
	})
}
