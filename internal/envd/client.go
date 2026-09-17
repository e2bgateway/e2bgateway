// Package envd provides a client for the envd ConnectRPC data plane.
//
// envd is the E2B daemon running inside sandbox containers on port 49983.
// It exposes ConnectRPC services for process execution and filesystem operations.
// This client communicates with envd using JSON codec (not binary protobuf)
// and supports both unary and streaming RPCs.
//
// Services:
//   - process.Process: Start, Connect, List processes
//   - filesystem.Filesystem: Stat, ListDir, MakeDir, Remove, Move
//   - File transfer: Upload/Download via REST API (not ConnectRPC)
package envd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client communicates with the envd daemon running inside a sandbox container.
type Client struct {
	baseURL     string
	accessToken string
	sandboxID   string
	httpClient  *http.Client
}

// ClientConfig holds configuration for creating a new envd client.
type ClientConfig struct {
	BaseURL     string        // e.g., "http://10.244.0.7:49983"
	AccessToken string        // envd access token for authentication
	SandboxID   string        // sandbox ID for routing
	Timeout     time.Duration // HTTP client timeout (default: 30s)
}

// NewClient creates a new envd client.
func NewClient(cfg ClientConfig) *Client {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return &Client{
		baseURL:     cfg.BaseURL,
		accessToken: cfg.AccessToken,
		sandboxID:   cfg.SandboxID,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// doConnectRPC performs a ConnectRPC unary call.
// service: e.g., "process.Process"
// method: e.g., "Start"
// req: request message (will be JSON-encoded)
// resp: response message pointer (will be JSON-decoded)
func (c *Client) doConnectRPC(ctx context.Context, service, method string, req, resp interface{}) error {
	url := fmt.Sprintf("%s/%s/%s", c.baseURL, service, method)

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	// Set required headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Connect-Protocol-Version", "1")
	httpReq.Header.Set("E2b-Sandbox-Id", c.sandboxID)
	httpReq.Header.Set("E2b-Sandbox-Port", "49983")
	if c.accessToken != "" {
		httpReq.Header.Set("X-Access-Token", c.accessToken)
		httpReq.Header.Set("Authorization", "Bearer "+c.accessToken)
	}

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(httpResp.Body)
		return fmt.Errorf("unexpected status %d: %s", httpResp.StatusCode, string(bodyBytes))
	}

	if resp != nil {
		if err := json.NewDecoder(httpResp.Body).Decode(resp); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}

	return nil
}

// doConnectRPCStream performs a ConnectRPC server-streaming call.
// Returns a reader for the streaming response.
func (c *Client) doConnectRPCStream(ctx context.Context, service, method string, req interface{}) (io.ReadCloser, error) {
	url := fmt.Sprintf("%s/%s/%s", c.baseURL, service, method)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	// Set required headers
	httpReq.Header.Set("Content-Type", "application/connect+json")
	httpReq.Header.Set("Connect-Protocol-Version", "1")
	httpReq.Header.Set("E2b-Sandbox-Id", c.sandboxID)
	httpReq.Header.Set("E2b-Sandbox-Port", "49983")
	if c.accessToken != "" {
		httpReq.Header.Set("X-Access-Token", c.accessToken)
		httpReq.Header.Set("Authorization", "Bearer "+c.accessToken)
	}

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		return nil, fmt.Errorf("unexpected status %d: %s", httpResp.StatusCode, string(bodyBytes))
	}

	return httpResp.Body, nil
}

// BaseURL returns the client's base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// SandboxID returns the client's sandbox ID.
func (c *Client) SandboxID() string {
	return c.sandboxID
}
