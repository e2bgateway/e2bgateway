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
// req: request message (will be JSON-encoded with envelope framing)
// resp: response message pointer (will be JSON-decoded from envelope)
func (c *Client) doConnectRPC(ctx context.Context, service, method string, req, resp interface{}) error {
	url := fmt.Sprintf("%s/%s/%s", c.baseURL, service, method)

	// ConnectRPC unary calls use envelope framing.
	envelope, err := EncodeEnvelope(EnvelopeFlagNone, req)
	if err != nil {
		return fmt.Errorf("encoding request envelope: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(envelope))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
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
		return fmt.Errorf("sending request: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(httpResp.Body)
		return fmt.Errorf("unexpected status %d: %s", httpResp.StatusCode, string(bodyBytes))
	}

	if resp != nil {
		// Unary response is also envelope-framed
		respEnvelope, err := DecodeEnvelope(httpResp.Body)
		if err != nil {
			// Fallback: try plain JSON decode if envelope decode fails
			// (for backward compatibility with servers that don't envelope unary responses)
			return fmt.Errorf("decoding response envelope: %w", err)
		}
		if err := respEnvelope.UnmarshalPayload(resp); err != nil {
			return fmt.Errorf("unmarshaling response payload: %w", err)
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

// SandboxID returns the client's sandbox ID.
func (c *Client) SandboxID() string {
	return c.sandboxID
}
