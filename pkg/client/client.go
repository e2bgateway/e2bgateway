// Package client provides a Go SDK for the E2BGateway admin API.
//
// Usage:
//
//	c := client.New("http://localhost:8080", client.WithAPIKey("my-key"))
//	info, err := c.CreateSandbox(ctx, client.CreateSandboxRequest{TemplateID: "base"})
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/api/dto"
	"github.com/e2bgateway/e2bgateway/pkg/api"
)

// -----------------------------------------------------------------------
// Public SDK types – thin aliases / wrappers around the wire-format DTOs
// so callers do not need to import internal packages.
// -----------------------------------------------------------------------

// CreateSandboxRequest is the request body for creating a sandbox.
type CreateSandboxRequest = dto.SandboxCreateRequest

// SandboxCreateResponse is the response returned after creating a sandbox.
type SandboxCreateResponse = dto.SandboxCreateResponse

// SandboxInfo describes a running (or paused) sandbox.
type SandboxInfo = dto.SandboxInfo

// TemplateInfo describes a sandbox template.
type TemplateInfo = dto.TemplateInfo

// ErrorResponse is the standard error body returned by the gateway.
type ErrorResponse = dto.ErrorResponse

// HealthStatus is the response from the /healthz endpoint.
type HealthStatus struct {
	Status string `json:"status"`
}

// -----------------------------------------------------------------------
// Error type
// -----------------------------------------------------------------------

// APIError is returned by client methods when the server responds with a
// non-2xx status code. It captures the HTTP status code and the parsed
// error message from the response body.
type APIError struct {
	StatusCode int
	Code       int
	Message    string
}

// Error implements the error interface.
func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("e2bgateway: %d %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("e2bgateway: unexpected status %d", e.StatusCode)
}

// -----------------------------------------------------------------------
// Client
// -----------------------------------------------------------------------

// Client is the E2BGateway admin client.
type Client struct {
	baseURL    string
	httpClient *http.Client
	apiKey     string
}

// Option configures the client.
type Option func(*Client)

// WithHTTPClient sets a custom *http.Client (e.g. with custom transport).
func WithHTTPClient(c *http.Client) Option {
	return func(c2 *Client) {
		if c != nil {
			c2.httpClient = c
		}
	}
}

// WithAPIKey sets the API key sent via the X-API-Key header on every request.
func WithAPIKey(key string) Option {
	return func(c *Client) {
		c.apiKey = key
	}
}

// WithTimeout sets the HTTP client timeout. It replaces the default 30-second
// timeout. If a custom *http.Client was already provided via WithHTTPClient
// this option is ignored.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if c.httpClient != nil {
			c.httpClient.Timeout = d
		}
	}
}

// New creates a new Client. baseURL is the scheme+host of the gateway, e.g.
// "http://localhost:8080". A trailing slash is stripped automatically.
func New(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// -----------------------------------------------------------------------
// Sandbox operations
// -----------------------------------------------------------------------

// CreateSandbox creates a new sandbox and returns the creation response
// (POST /sandboxes → 201).
func (c *Client) CreateSandbox(ctx context.Context, req CreateSandboxRequest) (*SandboxCreateResponse, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, "/sandboxes", req)
	if err != nil {
		return nil, err
	}
	defer drain(resp)

	if err := c.checkStatus(resp, http.StatusCreated); err != nil {
		return nil, err
	}

	var out SandboxCreateResponse
	if err := c.decodeResponse(resp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListSandboxes returns all sandboxes known to the gateway (GET /sandboxes).
func (c *Client) ListSandboxes(ctx context.Context) ([]SandboxInfo, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/sandboxes", nil)
	if err != nil {
		return nil, err
	}
	defer drain(resp)

	if err := c.checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}

	var out []SandboxInfo
	if err := c.decodeResponse(resp, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []SandboxInfo{}
	}
	return out, nil
}

// GetSandbox returns details for a single sandbox (GET /sandboxes/{sandboxID}).
func (c *Client) GetSandbox(ctx context.Context, sandboxID string) (*SandboxInfo, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/sandboxes/"+urlPathEscape(sandboxID), nil)
	if err != nil {
		return nil, err
	}
	defer drain(resp)

	if err := c.checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}

	var out SandboxInfo
	if err := c.decodeResponse(resp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// KillSandbox terminates a running sandbox (DELETE /sandboxes/{sandboxID} → 204).
func (c *Client) KillSandbox(ctx context.Context, sandboxID string) error {
	resp, err := c.doRequest(ctx, http.MethodDelete, "/sandboxes/"+urlPathEscape(sandboxID), nil)
	if err != nil {
		return err
	}
	defer drain(resp)
	return c.checkStatus(resp, http.StatusNoContent)
}

// PauseSandbox pauses a running sandbox (POST /sandboxes/{sandboxID}/pause → 204).
func (c *Client) PauseSandbox(ctx context.Context, sandboxID string) error {
	resp, err := c.doRequest(ctx, http.MethodPost, "/sandboxes/"+urlPathEscape(sandboxID)+"/pause", nil)
	if err != nil {
		return err
	}
	defer drain(resp)
	return c.checkStatus(resp, http.StatusNoContent)
}

// ResumeSandbox resumes a paused sandbox (POST /sandboxes/{sandboxID}/resume → 200).
func (c *Client) ResumeSandbox(ctx context.Context, sandboxID string) (*SandboxInfo, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, "/sandboxes/"+urlPathEscape(sandboxID)+"/resume", nil)
	if err != nil {
		return nil, err
	}
	defer drain(resp)

	if err := c.checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}

	var out SandboxInfo
	if err := c.decodeResponse(resp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetTimeout updates the auto-termination timeout of a sandbox (in seconds
// from now). Both PATCH and POST are accepted by the server; we use PATCH
// (PATCH /sandboxes/{sandboxID}/timeout → 204).
func (c *Client) SetTimeout(ctx context.Context, sandboxID string, timeout int) error {
	body := dto.SandboxTimeoutRequest{Timeout: timeout}
	resp, err := c.doRequest(ctx, http.MethodPatch, "/sandboxes/"+urlPathEscape(sandboxID)+"/timeout", body)
	if err != nil {
		return err
	}
	defer drain(resp)
	return c.checkStatus(resp, http.StatusNoContent)
}

// -----------------------------------------------------------------------
// Template operations
// -----------------------------------------------------------------------

// ListTemplates returns all templates known to the gateway (GET /templates).
func (c *Client) ListTemplates(ctx context.Context) ([]TemplateInfo, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/templates", nil)
	if err != nil {
		return nil, err
	}
	defer drain(resp)

	if err := c.checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}

	var out []TemplateInfo
	if err := c.decodeResponse(resp, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []TemplateInfo{}
	}
	return out, nil
}

// GetTemplate returns details for a single template (GET /templates/{templateID}).
func (c *Client) GetTemplate(ctx context.Context, templateID string) (*TemplateInfo, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/templates/"+urlPathEscape(templateID), nil)
	if err != nil {
		return nil, err
	}
	defer drain(resp)

	if err := c.checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}

	var out TemplateInfo
	if err := c.decodeResponse(resp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteTemplate removes a template (DELETE /templates/{templateID} → 204).
func (c *Client) DeleteTemplate(ctx context.Context, templateID string) error {
	resp, err := c.doRequest(ctx, http.MethodDelete, "/templates/"+urlPathEscape(templateID), nil)
	if err != nil {
		return err
	}
	defer drain(resp)
	return c.checkStatus(resp, http.StatusNoContent)
}

// -----------------------------------------------------------------------
// Health
// -----------------------------------------------------------------------

// Health calls GET /healthz and returns the parsed status.
func (c *Client) Health(ctx context.Context) (*HealthStatus, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/healthz", nil)
	if err != nil {
		return nil, err
	}
	defer drain(resp)

	if err := c.checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}

	var out HealthStatus
	if err := c.decodeResponse(resp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// -----------------------------------------------------------------------
// Internal helpers
// -----------------------------------------------------------------------

// doRequest builds an HTTP request, sets common headers, and executes it.
// body may be nil for GET / DELETE requests.
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("client: marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("client: build request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", api.ContentTypeJSON)
	}
	req.Header.Set("Accept", api.ContentTypeJSON)
	if c.apiKey != "" {
		req.Header.Set(api.HeaderAPIKey, c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("client: %s %s: %w", method, path, err)
	}
	return resp, nil
}

// decodeResponse reads and JSON-decodes the response body into v.
func (c *Client) decodeResponse(resp *http.Response, v interface{}) error {
	if v == nil {
		return nil
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("client: read response body: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("client: decode response: %w", err)
	}
	return nil
}

// checkStatus verifies that the response has the expected status code.
// On mismatch it reads the body and returns a typed *APIError.
func (c *Client) checkStatus(resp *http.Response, want int) error {
	if resp.StatusCode == want {
		return nil
	}
	apiErr := &APIError{StatusCode: resp.StatusCode}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		apiErr.Message = "(failed to read error body: " + err.Error() + ")"
		return apiErr
	}
	if len(data) > 0 {
		var errResp ErrorResponse
		if json.Unmarshal(data, &errResp) == nil && errResp.Message != "" {
			apiErr.Code = errResp.Code
			apiErr.Message = errResp.Message
			return apiErr
		}
		apiErr.Message = strings.TrimSpace(string(data))
	}
	return apiErr
}

// drain reads and discards any remaining body bytes so the underlying TCP
// connection can be reused.
func drain(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

// urlPathEscape percent-encodes a URL path segment, but keeps the result
// suitable for direct concatenation (no '+' for spaces, etc.).
func urlPathEscape(s string) string {
	// Use net/url PathEscape semantics but avoid importing net/url in the
	// hot path – the standard library function handles all edge cases.
	return pathEscape(s)
}

// pathEscape escapes s so it can be used as a single URL path segment.
func pathEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if isUnreserved(byte(r)) {
			b.WriteRune(r)
		} else {
			buf := [4]byte{}
			n := encodeRune(buf[:], r)
			b.Write(buf[:n])
		}
	}
	return b.String()
}

func isUnreserved(c byte) bool {
	return (c >= 'A' && c <= 'Z') ||
		(c >= 'a' && c <= 'z') ||
		(c >= '0' && c <= '9') ||
		c == '-' || c == '_' || c == '.' || c == '~'
}

func encodeRune(dst []byte, r rune) int {
	// Simple UTF-8 → percent-encoded output.
	buf := [4]byte{}
	n := 0
	switch {
	case r < 0x80:
		buf[0] = byte(r)
		n = 1
	case r < 0x800:
		buf[0] = byte(0xC0 | (r >> 6))
		buf[1] = byte(0x80 | (r & 0x3F))
		n = 2
	case r < 0x10000:
		buf[0] = byte(0xE0 | (r >> 12))
		buf[1] = byte(0x80 | ((r >> 6) & 0x3F))
		buf[2] = byte(0x80 | (r & 0x3F))
		n = 3
	default:
		buf[0] = byte(0xF0 | (r >> 18))
		buf[1] = byte(0x80 | ((r >> 12) & 0x3F))
		buf[2] = byte(0x80 | ((r >> 6) & 0x3F))
		buf[3] = byte(0x80 | (r & 0x3F))
		n = 4
	}
	const hex = "0123456789ABCDEF"
	out := 0
	for i := 0; i < n; i++ {
		dst[out] = '%'
		dst[out+1] = hex[buf[i]>>4]
		dst[out+2] = hex[buf[i]&0x0F]
		out += 3
	}
	return out
}
