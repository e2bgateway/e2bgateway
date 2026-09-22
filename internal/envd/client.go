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
// method: e.g., "List"
// req: request message (will be JSON-encoded)
// resp: response message pointer (will be JSON-decoded)
//
// Connect protocol for unary RPCs uses plain JSON bodies — no envelope
// framing. Envelope framing is only used for server-streaming RPCs
// (see doConnectRPCStream). See https://connectrpc.com/docs/protocol/.
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

	// Set required headers. Connect unary uses plain application/json
	// (not application/connect+json, which is for streaming).
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
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(httpResp.Body)
		return fmt.Errorf("unexpected status %d: %s", httpResp.StatusCode, string(bodyBytes))
	}

	if resp != nil {
		// Unary response is plain JSON (no envelope framing).
		bodyBytes, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return fmt.Errorf("reading response body: %w", err)
		}
		if err := json.Unmarshal(bodyBytes, resp); err != nil {
			return fmt.Errorf("decoding response (body=%q): %w", truncate(string(bodyBytes), 500), err)
		}
	}

	return nil
}

// doConnectRPCStream performs a ConnectRPC server-streaming call.
// Returns a reader for the streaming response.
// Both the request and response use ConnectRPC envelope format:
// [flags:1byte][length:4bytes][payload:length bytes]
func (c *Client) doConnectRPCStream(ctx context.Context, service, method string, req interface{}) (io.ReadCloser, error) {
	url := fmt.Sprintf("%s/%s/%s", c.baseURL, service, method)

	// ConnectRPC streaming requires envelope framing for both request and response.
	envelope, err := EncodeEnvelope(EnvelopeFlagNone, req)
	if err != nil {
		return nil, fmt.Errorf("encoding request envelope: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(envelope))
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
		_ = httpResp.Body.Close()
		return nil, fmt.Errorf("unexpected status %d: %s", httpResp.StatusCode, string(bodyBytes))
	}

	return httpResp.Body, nil
}

// BaseURL returns the client's base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// truncate shortens a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// SandboxID returns the client's sandbox ID.
func (c *Client) SandboxID() string {
	return c.sandboxID
}
