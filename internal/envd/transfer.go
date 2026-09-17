package envd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
)

// UploadFile uploads a file to the sandbox via REST API.
// envd uses REST API for file transfer, not ConnectRPC.
func (c *Client) UploadFile(ctx context.Context, path string, reader io.Reader) error {
	uploadURL := fmt.Sprintf("%s/files", c.baseURL)

	// Create multipart form
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add metadata part with path
	metadata := fmt.Sprintf(`{"path":%q}`, path)
	if err := writer.WriteField("metadata", metadata); err != nil {
		return fmt.Errorf("writing metadata field: %w", err)
	}

	// Add file part
	filename := filepath.Base(path)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return fmt.Errorf("creating form file: %w", err)
	}

	if _, err := io.Copy(part, reader); err != nil {
		return fmt.Errorf("copying file data: %w", err)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("closing multipart writer: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, &buf)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("E2b-Sandbox-Id", c.sandboxID)
	req.Header.Set("E2b-Sandbox-Port", "49983")
	if c.accessToken != "" {
		req.Header.Set("X-Access-Token", c.accessToken)
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}

	// Send request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// DownloadFile downloads a file from the sandbox via REST API.
func (c *Client) DownloadFile(ctx context.Context, path string) (io.ReadCloser, error) {
	downloadURL := fmt.Sprintf("%s/files?%s", c.baseURL, url.Values{"path": {path}}.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("E2b-Sandbox-Id", c.sandboxID)
	req.Header.Set("E2b-Sandbox-Port", "49983")
	if c.accessToken != "" {
		req.Header.Set("X-Access-Token", c.accessToken)
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("download failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return resp.Body, nil
}

// UploadFileOptions contains options for uploading a file.
type UploadFileOptions struct {
	Path     string
	Reader   io.Reader
	Username string
	Gzip     bool
	Metadata map[string]string
}

// UploadFileWithOptions uploads a file with additional options.
func (c *Client) UploadFileWithOptions(ctx context.Context, opts UploadFileOptions) error {
	uploadURL := fmt.Sprintf("%s/files", c.baseURL)

	// Create multipart form
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add metadata part with path and custom metadata
	metadataJSON := fmt.Sprintf(`{"path":%q`, opts.Path)
	for k, v := range opts.Metadata {
		metadataJSON += fmt.Sprintf(`,%q:%q`, k, v)
	}
	metadataJSON += `}`

	if err := writer.WriteField("metadata", metadataJSON); err != nil {
		return fmt.Errorf("writing metadata field: %w", err)
	}

	// Add file part
	filename := filepath.Base(opts.Path)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return fmt.Errorf("creating form file: %w", err)
	}

	if _, err := io.Copy(part, opts.Reader); err != nil {
		return fmt.Errorf("copying file data: %w", err)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("closing multipart writer: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, &buf)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("E2b-Sandbox-Id", c.sandboxID)
	req.Header.Set("E2b-Sandbox-Port", "49983")
	if c.accessToken != "" {
		req.Header.Set("X-Access-Token", c.accessToken)
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}

	if opts.Username != "" {
		req.Header.Set("X-Username", opts.Username)
	}

	if opts.Gzip {
		req.Header.Set("Content-Encoding", "gzip")
	}

	// Send request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// DownloadFileToWriter downloads a file and writes it to the provided writer.
func (c *Client) DownloadFileToWriter(ctx context.Context, path string, writer io.Writer) error {
	reader, err := c.DownloadFile(ctx, path)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()

	if _, err := io.Copy(writer, reader); err != nil {
		return fmt.Errorf("copying file data: %w", err)
	}

	return nil
}
