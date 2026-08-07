// Package e2bcloud implements the E2B Cloud adapter for E2BGateway.
// It acts as a transparent proxy to the official E2B SaaS API.
package e2bcloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/api/dto"
)

// Client is an HTTP client for the E2B Cloud API.
type Client struct {
	baseURL    string
	apiKey     string
	maxRetries int
	httpClient *http.Client
}

// ClientConfig holds configuration for the E2B Cloud client.
type ClientConfig struct {
	Endpoint   string
	APIKey     string
	Timeout    time.Duration
	MaxRetries int
}

// NewClient creates a new E2B Cloud API client.
func NewClient(cfg ClientConfig) *Client {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://api.e2b.dev"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 60 * time.Second
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}

	return &Client{
		baseURL:    cfg.Endpoint,
		apiKey:     cfg.APIKey,
		maxRetries: cfg.MaxRetries,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 50,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// buildRequest creates an HTTP request with proper headers and body.
func (c *Client) buildRequest(ctx context.Context, method, path string, bodyBytes []byte) (*http.Request, error) {
	var reqBody io.Reader
	if bodyBytes != nil {
		reqBody = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// handleResponse processes the HTTP response and returns appropriate error or nil.
func (c *Client) handleResponse(resp *http.Response, result interface{}) (shouldRetry bool, err error) {
	// Check if we should retry based on status code
	isRetryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500

	if resp.StatusCode >= 400 {
		var errResp dto.ErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		_ = resp.Body.Close()
		apiErr := &APIError{
			StatusCode: resp.StatusCode,
			Code:       errResp.Code,
			Message:    errResp.Message,
		}
		return isRetryable, apiErr
	}

	if result != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			_ = resp.Body.Close()
			return false, fmt.Errorf("decoding response: %w", err)
		}
	}
	_ = resp.Body.Close()
	return false, nil
}

// do executes an HTTP request with auth headers, error handling, and retry logic.
func (c *Client) do(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	var bodyBytes []byte
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshaling request: %w", err)
		}
		bodyBytes = data
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		// Check context before retrying
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("context canceled: %w", err)
		}

		// Exponential backoff: 100ms, 200ms, 400ms, 800ms...
		if attempt > 0 {
			backoff := time.Duration(1<<(attempt-1)) * 100 * time.Millisecond
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return fmt.Errorf("context canceled during backoff: %w", ctx.Err())
			}
		}

		req, err := c.buildRequest(ctx, method, path, bodyBytes)
		if err != nil {
			return fmt.Errorf("creating request: %w", err)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("executing request: %w", err)
			// Network errors are retryable
			continue
		}

		shouldRetry, err := c.handleResponse(resp, result)
		if err != nil {
			if shouldRetry {
				lastErr = err
				continue
			}
			return err
		}
		return nil
	}

	// All retries exhausted
	if lastErr != nil {
		return fmt.Errorf("request failed after %d attempts: %w", c.maxRetries+1, lastErr)
	}
	return fmt.Errorf("request failed after %d attempts", c.maxRetries+1)
}

// doRaw executes an HTTP request and returns the raw response body.
func (c *Client) doRaw(ctx context.Context, method, path string, body io.Reader, contentType string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("X-API-Key", c.apiKey)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var errResp dto.ErrorResponse
		_ = json.Unmarshal(respBody, &errResp)
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Code:       errResp.Code,
			Message:    errResp.Message,
		}
	}

	return respBody, nil
}

// --- Sandbox Operations ---

func (c *Client) CreateSandbox(ctx context.Context, req *dto.SandboxCreateRequest) (*dto.SandboxCreateResponse, error) {
	var resp dto.SandboxCreateResponse
	if err := c.do(ctx, http.MethodPost, "/sandboxes", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) ListSandboxes(ctx context.Context) ([]dto.SandboxInfo, error) {
	var resp []dto.SandboxInfo
	if err := c.do(ctx, http.MethodGet, "/sandboxes", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) GetSandbox(ctx context.Context, sandboxID string) (*dto.SandboxInfo, error) {
	var resp dto.SandboxInfo
	if err := c.do(ctx, http.MethodGet, "/sandboxes/"+sandboxID, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) KillSandbox(ctx context.Context, sandboxID string) error {
	return c.do(ctx, http.MethodDelete, "/sandboxes/"+sandboxID, nil, nil)
}

func (c *Client) PauseSandbox(ctx context.Context, sandboxID string) error {
	return c.do(ctx, http.MethodPost, "/sandboxes/"+sandboxID+"/pause", nil, nil)
}

func (c *Client) ResumeSandbox(ctx context.Context, sandboxID string, req *dto.SandboxResumeRequest) (*dto.SandboxCreateResponse, error) {
	var resp dto.SandboxCreateResponse
	if err := c.do(ctx, http.MethodPost, "/sandboxes/"+sandboxID+"/resume", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) SetTimeout(ctx context.Context, sandboxID string, req *dto.SandboxTimeoutRequest) error {
	return c.do(ctx, http.MethodPost, "/sandboxes/"+sandboxID+"/timeout", req, nil)
}

// --- Code Execution ---

func (c *Client) ExecuteCode(ctx context.Context, sandboxID string, req *dto.CodeExecRequest) (*dto.CodeExecResult, error) {
	var resp dto.CodeExecResult
	if err := c.do(ctx, http.MethodPost, "/sandboxes/"+sandboxID+"/code", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) RunCommand(ctx context.Context, sandboxID string, req *dto.CommandRequest) (*dto.CommandResult, error) {
	var resp dto.CommandResult
	if err := c.do(ctx, http.MethodPost, "/sandboxes/"+sandboxID+"/commands", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// --- File Operations ---

func (c *Client) WriteFile(ctx context.Context, sandboxID string, req *dto.FileWriteRequest) error {
	return c.do(ctx, http.MethodPost, "/sandboxes/"+sandboxID+"/files", req, nil)
}

func (c *Client) ReadFile(ctx context.Context, sandboxID string, path string) ([]byte, error) {
	data, err := c.doRaw(ctx, http.MethodGet, "/sandboxes/"+sandboxID+"/files?path="+path, nil, "")
	return data, err
}

func (c *Client) ListFiles(ctx context.Context, sandboxID string, req *dto.FileListRequest) (*dto.FileListResponse, error) {
	var resp dto.FileListResponse
	if err := c.do(ctx, http.MethodPost, "/sandboxes/"+sandboxID+"/files/list", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) MakeDir(ctx context.Context, sandboxID string, req *dto.MakeDirRequest) error {
	return c.do(ctx, http.MethodPost, "/sandboxes/"+sandboxID+"/files/make-dir", req, nil)
}

func (c *Client) RemoveFile(ctx context.Context, sandboxID string, req *dto.FileRemoveRequest) error {
	return c.do(ctx, http.MethodPost, "/sandboxes/"+sandboxID+"/files/remove", req, nil)
}

func (c *Client) UploadFile(ctx context.Context, sandboxID string, path string, content io.Reader) error {
	_, err := c.doRaw(ctx, http.MethodPost, "/sandboxes/"+sandboxID+"/files/upload?path="+path, content, "application/octet-stream")
	return err
}

func (c *Client) DownloadFile(ctx context.Context, sandboxID string, path string) ([]byte, error) {
	data, err := c.doRaw(ctx, http.MethodGet, "/sandboxes/"+sandboxID+"/files/download?path="+path, nil, "")
	return data, err
}

// --- Processes ---

func (c *Client) ListProcesses(ctx context.Context, sandboxID string) ([]dto.ProcessInfo, error) {
	var resp []dto.ProcessInfo
	if err := c.do(ctx, http.MethodGet, "/sandboxes/"+sandboxID+"/processes", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) KillProcess(ctx context.Context, sandboxID, processID string) error {
	return c.do(ctx, http.MethodPost, "/sandboxes/"+sandboxID+"/processes/"+processID+"/kill", nil, nil)
}

func (c *Client) SendStdin(ctx context.Context, sandboxID, processID string, req *dto.SendStdinRequest) error {
	return c.do(ctx, http.MethodPost, "/sandboxes/"+sandboxID+"/processes/"+processID+"/stdin", req, nil)
}

// --- Ports ---

func (c *Client) ListPorts(ctx context.Context, sandboxID string) (*dto.PortsResponse, error) {
	var resp dto.PortsResponse
	if err := c.do(ctx, http.MethodGet, "/sandboxes/"+sandboxID+"/ports", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetPortURL(ctx context.Context, sandboxID string, port int) (*dto.PortURLResponse, error) {
	var resp dto.PortURLResponse
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/sandboxes/%s/ports/%d", sandboxID, port), nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// --- Snapshots ---

func (c *Client) CreateSnapshot(ctx context.Context, sandboxID string, req *dto.SnapshotCreateRequest) (*dto.SnapshotInfo, error) {
	var resp dto.SnapshotInfo
	if err := c.do(ctx, http.MethodPost, "/sandboxes/"+sandboxID+"/snapshots", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) ListSnapshots(ctx context.Context, sandboxID string) ([]dto.SnapshotInfo, error) {
	var resp []dto.SnapshotInfo
	if err := c.do(ctx, http.MethodGet, "/sandboxes/"+sandboxID+"/snapshots", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// --- Access Token ---

func (c *Client) GetAccessToken(ctx context.Context, sandboxID string) (*dto.AccessTokenResponse, error) {
	var resp dto.AccessTokenResponse
	if err := c.do(ctx, http.MethodPost, "/sandboxes/"+sandboxID+"/access-token", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// --- Environment Variables ---

func (c *Client) SetEnvs(ctx context.Context, sandboxID string, envs map[string]string) error {
	return c.do(ctx, http.MethodPost, "/sandboxes/"+sandboxID+"/envs", map[string]interface{}{"envs": envs}, nil)
}

// --- Logs ---

func (c *Client) GetLogs(ctx context.Context, sandboxID string) ([]dto.LogEntry, error) {
	var resp dto.SandboxLogsResponse
	if err := c.do(ctx, http.MethodGet, "/sandboxes/"+sandboxID+"/logs", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Logs, nil
}

// --- File Move ---

func (c *Client) MoveFile(ctx context.Context, sandboxID, src, dst string) error {
	return c.do(ctx, http.MethodPost, "/sandboxes/"+sandboxID+"/filesystem/move", &dto.MoveFileRequest{
		Source:      src,
		Destination: dst,
	}, nil)
}

// --- Template Tags ---

func (c *Client) CreateTag(ctx context.Context, templateID string, req *dto.CreateTagRequest) (*dto.TagInfo, error) {
	var resp dto.TagInfo
	if err := c.do(ctx, http.MethodPost, "/templates/"+templateID+"/tags", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) ListTags(ctx context.Context, templateID string) ([]dto.TagInfo, error) {
	var resp []dto.TagInfo
	if err := c.do(ctx, http.MethodGet, "/templates/"+templateID+"/tags", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) DeleteTag(ctx context.Context, templateID, tagName string) error {
	return c.do(ctx, http.MethodDelete, "/templates/"+templateID+"/tags/"+tagName, nil, nil)
}

// --- Templates ---

func (c *Client) ListTemplates(ctx context.Context) ([]dto.TemplateInfo, error) {
	var resp []dto.TemplateInfo
	if err := c.do(ctx, http.MethodGet, "/templates", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) GetTemplate(ctx context.Context, templateID string) (*dto.TemplateInfo, error) {
	var resp dto.TemplateInfo
	if err := c.do(ctx, http.MethodGet, "/templates/"+templateID, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) CreateTemplate(ctx context.Context, req *dto.TemplateBuildRequest) (*dto.TemplateBuildResponse, error) {
	var resp dto.TemplateBuildResponse
	if err := c.do(ctx, http.MethodPost, "/templates", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) DeleteTemplate(ctx context.Context, templateID string) error {
	return c.do(ctx, http.MethodDelete, "/templates/"+templateID, nil, nil)
}

func (c *Client) GetBuildStatus(ctx context.Context, templateID, buildID string) (*dto.BuildStatusResponse, error) {
	var resp dto.BuildStatusResponse
	if err := c.do(ctx, http.MethodPost, "/templates/"+templateID+"/builds/"+buildID+"/status", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) CreateAlias(ctx context.Context, templateID string, req *dto.AliasRequest) error {
	return c.do(ctx, http.MethodPost, "/templates/"+templateID+"/aliases", req, nil)
}

func (c *Client) DeleteAlias(ctx context.Context, templateID, alias string) error {
	return c.do(ctx, http.MethodDelete, "/templates/"+templateID+"/aliases/"+alias, nil, nil)
}

// --- Warm Pools ---

func (c *Client) ListWarmPools(ctx context.Context) ([]dto.WarmPoolInfo, error) {
	var resp []dto.WarmPoolInfo
	if err := c.do(ctx, http.MethodGet, "/warm-pools", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) CreateWarmPool(ctx context.Context, req *dto.WarmPoolCreateRequest) (*dto.WarmPoolInfo, error) {
	var resp dto.WarmPoolInfo
	if err := c.do(ctx, http.MethodPost, "/warm-pools", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) DeleteWarmPool(ctx context.Context, warmPoolID string) error {
	return c.do(ctx, http.MethodDelete, "/warm-pools/"+warmPoolID, nil, nil)
}

func (c *Client) UpdateWarmPoolSize(ctx context.Context, warmPoolID string, req *dto.WarmPoolSizeRequest) error {
	return c.do(ctx, http.MethodPost, "/warm-pools/"+warmPoolID+"/size", req, nil)
}

// --- API Error ---

// APIError represents an error from the E2B Cloud API.
type APIError struct {
	StatusCode int
	Code       int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("e2b api error (status=%d, code=%d): %s", e.StatusCode, e.Code, e.Message)
}

// IsNotFound returns true if the error is a 404.
func (e *APIError) IsNotFound() bool {
	return e.StatusCode == http.StatusNotFound
}
