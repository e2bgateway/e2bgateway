// Package opensandbox implements the E2BGateway adapter for alibaba/OpenSandbox.
// It uses the official OpenSandbox Go SDK for lifecycle and execution operations.
//
// Architecture:
//   - LifecycleClient: manages sandbox lifecycle (create, list, get, delete, pause, resume)
//   - ExecdClient: handles code execution, command execution, and file operations
//   - E2B ID mapping: uses OpenSandbox's native sandbox IDs
package opensandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/cache"
)

const defaultLanguage = "python"

// lifecycleClient is the subset of OpenSandbox LifecycleClient methods used
// by the adapter. Defining it as an interface enables unit testing with mocks.
type lifecycleClient interface {
	ListSandboxes(ctx context.Context, opts opensandbox.ListOptions) (*opensandbox.ListSandboxesResponse, error)
	CreateSandbox(ctx context.Context, req opensandbox.CreateSandboxRequest) (*opensandbox.SandboxInfo, error)
	GetSandbox(ctx context.Context, id string) (*opensandbox.SandboxInfo, error)
	DeleteSandbox(ctx context.Context, id string) error
	PauseSandbox(ctx context.Context, id string) error
	ResumeSandbox(ctx context.Context, id string) error
	RenewExpiration(ctx context.Context, id string, expiresAt time.Time) (*opensandbox.RenewExpirationResponse, error)
	GetEndpoint(ctx context.Context, sandboxID string, port int, useServerProxy *bool) (*opensandbox.Endpoint, error)
	GetSignedEndpoint(ctx context.Context, sandboxID string, port int, expires int64) (*opensandbox.Endpoint, error)
}

// Adapter implements adapter.SandboxAdapter using the OpenSandbox client.
type Adapter struct {
	name      string
	lifecycle lifecycleClient
	baseURL   string
	apiKey    string

	// TemplateToImage maps E2B template IDs to OpenSandbox image URIs.
	templateToImage map[string]string

	// Per-sandbox ExecdClient cache (keyed by sandbox ID).
	execdClients   map[string]*opensandbox.ExecdClient
	execdClientsMu sync.RWMutex

	// Token cache for access token generation and validation.
	tokenCache *cache.Cache

	// useSignedEndpoint controls whether to use OpenSandbox server's OSEP-0011
	// signed endpoint (GetSignedEndpoint) instead of gateway-generated random
	// tokens. When true: token comes from server-side signing. When false:
	// gateway generates envd_{id}_{random} tokens.
	useSignedEndpoint bool

	// endpointHeaders stores server-returned auth headers per sandbox.
	// Key: sandboxID, Value: map[string]string (from Endpoint.Headers).
	endpointHeaders *cache.Cache

	// portTracker tracks opened ports per sandbox for ListPorts.
	// Key: sandboxID, Value: map[int]bool (port -> ready).
	portTracker   map[string]map[int]bool
	portTrackerMu sync.RWMutex
}

// AdapterConfig holds configuration for the OpenSandbox adapter.
type AdapterConfig struct {
	Name       string
	BaseURL    string
	APIKey     string
	ExecdURL   string
	ExecdToken string
	// TemplateToImage maps E2B template IDs to OpenSandbox image URIs.
	// If a template ID is not in this map, it is used directly as the image URI.
	TemplateToImage map[string]string
	// UseSignedEndpoint controls whether to use OpenSandbox server's OSEP-0011
	// signed endpoint (GetSignedEndpoint) instead of gateway-generated random
	// tokens. When true: token comes from server-side signing. When false
	// (default): gateway generates envd_{id}_{random} tokens.
	UseSignedEndpoint bool
}

// New creates a new OpenSandbox adapter.
func New(cfg AdapterConfig) (*Adapter, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("baseURL is required")
	}

	lifecycle := opensandbox.NewLifecycleClient(cfg.BaseURL, cfg.APIKey)

	templateToImage := cfg.TemplateToImage
	if templateToImage == nil {
		templateToImage = make(map[string]string)
	}

	return &Adapter{
		name:              cfg.Name,
		lifecycle:         lifecycle,
		baseURL:           cfg.BaseURL,
		apiKey:            cfg.APIKey,
		templateToImage:   templateToImage,
		execdClients:      make(map[string]*opensandbox.ExecdClient),
		tokenCache:        cache.New(10000, 1*time.Hour),
		useSignedEndpoint: cfg.UseSignedEndpoint,
		endpointHeaders:   cache.New(10000, 1*time.Hour),
		portTracker:       make(map[string]map[int]bool),
	}, nil
}

// waitRunning polls GetSandbox until the sandbox reaches StateRunning or the
// context is canceled. Mirrors the high-level SDK's waitForRunning behavior
// so that CreateSandbox returns a usable sandbox to E2B clients.
func (a *Adapter) waitRunning(ctx context.Context, sandboxID string) (*opensandbox.SandboxInfo, error) {
	deadline := time.Now().Add(60 * time.Second)
	delay := 200 * time.Millisecond
	for {
		info, err := a.lifecycle.GetSandbox(ctx, sandboxID)
		if err == nil {
			switch info.Status.State {
			case opensandbox.StateRunning:
				return info, nil
			case opensandbox.StateFailed, opensandbox.StateTerminated:
				return nil, fmt.Errorf("sandbox %q entered state %s", sandboxID, info.Status.State)
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for sandbox %q to become Running", sandboxID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		if delay < 2*time.Second {
			delay *= 2
		}
	}
}

// getOrCreateExecdClient returns the ExecdClient for a sandbox, creating one if needed.
// In dual-mode, the endpoint is obtained via GetSignedEndpoint (signed mode) or
// GetEndpoint (non-signed). The ExecdClient is initialized with:
//   - token from tokenCache (if available) via X-EXECD-ACCESS-TOKEN header
//   - headers from endpointHeaders cache merged with Endpoint.Headers from server
func (a *Adapter) getOrCreateExecdClient(ctx context.Context, sandboxID string) (*opensandbox.ExecdClient, error) {
	// Fast path: check under read lock
	a.execdClientsMu.RLock()
	if ec, ok := a.execdClients[sandboxID]; ok {
		a.execdClientsMu.RUnlock()
		return ec, nil
	}
	a.execdClientsMu.RUnlock()

	// Slow path: get endpoint (outside lock to avoid holding it during I/O).
	// Dual-mode: use GetSignedEndpoint when configured, otherwise GetEndpoint
	// via server proxy route (useServerProxy=true).
	var ep *opensandbox.Endpoint
	var err error

	if a.useSignedEndpoint {
		expires := time.Now().Add(1 * time.Hour).Unix()
		ep, err = a.lifecycle.GetSignedEndpoint(ctx, sandboxID, opensandbox.DefaultExecdPort, expires)
	} else {
		useProxy := true
		ep, err = a.lifecycle.GetEndpoint(ctx, sandboxID, opensandbox.DefaultExecdPort, &useProxy)
	}
	if err != nil {
		return nil, fmt.Errorf("getting execd endpoint for sandbox %q: %w", sandboxID, err)
	}

	execdURL := ep.Endpoint
	if !strings.HasPrefix(execdURL, "http") {
		execdURL = "http://" + execdURL
	}

	// Build options: merge cached headers with endpoint-returned headers.
	var opts []opensandbox.Option

	// Prefer cached headers (stored by GetAccessToken or GetEnvdEndpoint),
	// then overlay with freshly-returned endpoint headers.
	mergedHeaders := make(map[string]string)
	if cached, ok := a.endpointHeaders.Get(sandboxID); ok {
		if headers, ok := cached.(map[string]string); ok {
			for k, v := range headers {
				mergedHeaders[k] = v
			}
		}
	}
	for k, v := range ep.Headers {
		mergedHeaders[k] = v
	}
	if len(mergedHeaders) > 0 {
		opts = append(opts, opensandbox.WithHeaders(mergedHeaders))
	}

	// Pass cached access token (generated by GetAccessToken).
	token := ""
	if cached, ok := a.tokenCache.Get(sandboxID); ok {
		if tokenStr, ok := cached.(string); ok {
			token = tokenStr
		}
	}

	ec := opensandbox.NewExecdClient(execdURL, token, opts...)

	// Re-check under write lock to prevent race condition
	a.execdClientsMu.Lock()
	defer a.execdClientsMu.Unlock()
	if existing, ok := a.execdClients[sandboxID]; ok {
		// Another goroutine already created it; use that one
		return existing, nil
	}
	a.execdClients[sandboxID] = ec
	return ec, nil
}

// Name returns the adapter name.
func (a *Adapter) Name() string { return a.name }

// HealthCheck verifies connectivity.
func (a *Adapter) HealthCheck(ctx context.Context) error {
	// Try to list sandboxes as a health check
	_, err := a.lifecycle.ListSandboxes(ctx, opensandbox.ListOptions{PageSize: 1})
	return err
}

// --- Sandbox Lifecycle ---

func (a *Adapter) CreateSandbox(ctx context.Context, req *adapter.CreateSandboxRequest) (*adapter.Sandbox, error) {
	// Map E2B template ID to OpenSandbox image.
	imageURI := req.TemplateID
	if mapped, ok := a.templateToImage[req.TemplateID]; ok {
		imageURI = mapped
	}
	image := &opensandbox.ImageSpec{
		URI: imageURI,
	}

	// Use the SDK's default entrypoint (`tail -f /dev/null`) so the container
	// stays alive for interactive use. The previous `/bin/sh` exited instantly
	// with no stdin, killing the sandbox before execd could start.
	timeout := opensandbox.DefaultTimeoutSeconds
	sbx, err := a.lifecycle.CreateSandbox(ctx, opensandbox.CreateSandboxRequest{
		Image:          image,
		Entrypoint:     opensandbox.DefaultEntrypoint,
		ResourceLimits: opensandbox.DefaultResourceLimits,
		Timeout:        &timeout,
		Metadata:       req.Metadata,
	})
	if err != nil {
		return nil, fmt.Errorf("creating sandbox: %w", err)
	}

	// The low-level lifecycle.CreateSandbox returns as soon as the server
	// accepts the request (HTTP 202). E2B clients expect a usable sandbox
	// when POST /sandboxes returns, so poll until Running.
	if sbx.Status.State != opensandbox.StateRunning {
		info, err := a.waitRunning(ctx, sbx.ID)
		if err != nil {
			// Best-effort cleanup on failure.
			_ = a.lifecycle.DeleteSandbox(context.Background(), sbx.ID)
			return nil, err
		}
		sbx = info
	}

	return &adapter.Sandbox{
		SandboxID:  sbx.ID,
		TemplateID: req.TemplateID,
		Status:     mapState(sbx.Status.State),
		StartedAt:  sbx.CreatedAt,
		Metadata:   req.Metadata,
		Backend:    a.name,
	}, nil
}

func (a *Adapter) ListSandboxes(ctx context.Context, opts adapter.ListOptions) ([]*adapter.Sandbox, error) {
	list, err := a.lifecycle.ListSandboxes(ctx, opensandbox.ListOptions{
		PageSize: opts.Limit,
	})
	if err != nil {
		return nil, fmt.Errorf("listing sandboxes: %w", err)
	}

	var result []*adapter.Sandbox
	for _, sbx := range list.Items {
		result = append(result, &adapter.Sandbox{
			SandboxID:  sbx.ID,
			TemplateID: sbx.Image.URI,
			Status:     mapState(sbx.Status.State),
			StartedAt:  sbx.CreatedAt,
			Backend:    a.name,
		})
	}
	return result, nil
}

func (a *Adapter) GetSandbox(ctx context.Context, sandboxID string) (*adapter.Sandbox, error) {
	sbx, err := a.lifecycle.GetSandbox(ctx, sandboxID)
	if err != nil {
		return nil, fmt.Errorf("getting sandbox: %w", err)
	}

	return &adapter.Sandbox{
		SandboxID:  sbx.ID,
		TemplateID: sbx.Image.URI,
		Status:     mapState(sbx.Status.State),
		StartedAt:  sbx.CreatedAt,
		Backend:    a.name,
	}, nil
}

func (a *Adapter) KillSandbox(ctx context.Context, sandboxID string) error {
	if err := a.lifecycle.DeleteSandbox(ctx, sandboxID); err != nil {
		return err
	}
	// Cleanup execd client to prevent memory leak
	a.execdClientsMu.Lock()
	delete(a.execdClients, sandboxID)
	a.execdClientsMu.Unlock()
	// Cleanup port tracker
	a.portTrackerMu.Lock()
	delete(a.portTracker, sandboxID)
	a.portTrackerMu.Unlock()
	return nil
}

func (a *Adapter) PauseSandbox(ctx context.Context, sandboxID string) error {
	return a.lifecycle.PauseSandbox(ctx, sandboxID)
}

func (a *Adapter) ResumeSandbox(ctx context.Context, sandboxID string) (*adapter.Sandbox, error) {
	if err := a.lifecycle.ResumeSandbox(ctx, sandboxID); err != nil {
		return nil, err
	}
	return a.GetSandbox(ctx, sandboxID)
}

func (a *Adapter) SetTimeout(ctx context.Context, sandboxID string, timeout time.Duration) error {
	expiresAt := time.Now().Add(timeout)
	_, err := a.lifecycle.RenewExpiration(ctx, sandboxID, expiresAt)
	return err
}

// --- Code Execution ---

func (a *Adapter) ExecuteCode(ctx context.Context, sandboxID string, req *adapter.CodeExecutionRequest) (*adapter.CodeExecutionResult, error) {
	execClient, err := a.getOrCreateExecdClient(ctx, sandboxID)
	if err != nil {
		return nil, err
	}

	// Create or get execution context
	lang := req.Language
	if lang == "" {
		lang = defaultLanguage
	}

	// Execute code
	var stdout, stderr strings.Builder
	err = execClient.RunCommand(ctx, opensandbox.RunCommandRequest{
		Command: wrapCodeInCommand(req.Code, lang),
		Timeout: 30000, // 30 seconds default
	}, func(event opensandbox.StreamEvent) error {
		switch event.Event {
		case "stdout":
			stdout.WriteString(extractText(event.Data))
		case "stderr":
			stderr.WriteString(extractText(event.Data))
		}
		return nil
	})

	exitCode := 0
	if err != nil {
		exitCode = 1
	}

	return &adapter.CodeExecutionResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}, nil
}

func (a *Adapter) ExecuteCodeStream(ctx context.Context, sandboxID string, req *adapter.CodeExecutionRequest, stream adapter.CodeStream) error {
	execClient, err := a.getOrCreateExecdClient(ctx, sandboxID)
	if err != nil {
		return stream.Send(&adapter.StreamMessage{Type: "error", Data: err.Error()})
	}

	lang := req.Language
	if lang == "" {
		lang = "python"
	}

	err = execClient.RunCommand(ctx, opensandbox.RunCommandRequest{
		Command: wrapCodeInCommand(req.Code, lang),
		Timeout: 30000, // 30 seconds default
	}, func(event opensandbox.StreamEvent) error {
		return stream.Send(&adapter.StreamMessage{
			Type: event.Event,
			Data: extractText(event.Data),
		})
	})
	if err != nil {
		return stream.Send(&adapter.StreamMessage{Type: "error", Data: err.Error()})
	}

	return stream.Send(&adapter.StreamMessage{
		Type: "result",
		Data: map[string]interface{}{"exitCode": 0},
	})
}

func (a *Adapter) RunCommand(ctx context.Context, sandboxID string, req *adapter.CommandRequest) (*adapter.CommandResult, error) {
	execClient, err := a.getOrCreateExecdClient(ctx, sandboxID)
	if err != nil {
		return nil, err
	}

	command := req.Command
	if len(req.Args) > 0 {
		command = command + " " + strings.Join(req.Args, " ")
	}

	var stdout, stderr strings.Builder
	err = execClient.RunCommand(ctx, opensandbox.RunCommandRequest{
		Command: command,
		Timeout: 30000, // 30 seconds default
	}, func(event opensandbox.StreamEvent) error {
		switch event.Event {
		case "stdout":
			stdout.WriteString(extractText(event.Data))
		case "stderr":
			stderr.WriteString(extractText(event.Data))
		}
		return nil
	})

	exitCode := 0
	if err != nil {
		exitCode = 1
	}

	return &adapter.CommandResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}, nil
}

// --- Filesystem ---

func (a *Adapter) WriteFile(ctx context.Context, sandboxID string, req *adapter.FileWriteRequest) error {
	// Use UploadFile with bytes.NewReader to avoid shell injection via heredoc
	return a.UploadFile(ctx, sandboxID, &adapter.FileUploadRequest{
		Path:   req.Path,
		Reader: io.NopCloser(bytes.NewReader(req.Content)),
	})
}

func (a *Adapter) ReadFile(ctx context.Context, sandboxID string, path string) (*adapter.FileContent, error) {
	execClient, err := a.getOrCreateExecdClient(ctx, sandboxID)
	if err != nil {
		return nil, err
	}

	// Download file via execd client
	reader, err := execClient.DownloadFile(ctx, path, "")
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}

	return &adapter.FileContent{
		Path:    path,
		Content: data,
		Size:    int64(len(data)),
	}, nil
}

func (a *Adapter) UploadFile(ctx context.Context, sandboxID string, req *adapter.FileUploadRequest) error {
	execClient, err := a.getOrCreateExecdClient(ctx, sandboxID)
	if err != nil {
		return err
	}

	// The SDK's UploadFiles validates that Options.Metadata.Path is set;
	// the top-level FileName field is only the multipart filename and does
	// not drive the destination path inside the sandbox.
	return execClient.UploadFile(ctx, req.Reader, opensandbox.UploadFileOptions{
		FileName: req.Path,
		Metadata: opensandbox.FileMetadata{Path: req.Path},
	})
}

func (a *Adapter) DownloadFile(ctx context.Context, sandboxID string, path string) (io.ReadCloser, error) {
	execClient, err := a.getOrCreateExecdClient(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	return execClient.DownloadFile(ctx, path, "")
}

func (a *Adapter) ListFiles(ctx context.Context, sandboxID string, path string) ([]adapter.FileInfo, error) {
	execClient, err := a.getOrCreateExecdClient(ctx, sandboxID)
	if err != nil {
		return nil, err
	}

	entries, err := execClient.ListDirectory(ctx, path)
	if err != nil {
		return nil, err
	}

	var result []adapter.FileInfo
	for _, e := range entries {
		result = append(result, adapter.FileInfo{
			Name:  e.Path,
			Path:  e.Path,
			Size:  e.Size,
			IsDir: e.Type == "directory",
		})
	}
	return result, nil
}

func (a *Adapter) MakeDir(ctx context.Context, sandboxID string, path string) error {
	execClient, err := a.getOrCreateExecdClient(ctx, sandboxID)
	if err != nil {
		return err
	}
	return execClient.CreateDirectory(ctx, path, 0o755)
}

func (a *Adapter) RemoveFile(ctx context.Context, sandboxID string, path string) error {
	execClient, err := a.getOrCreateExecdClient(ctx, sandboxID)
	if err != nil {
		return err
	}
	return execClient.DeleteFiles(ctx, []string{path})
}

// --- Templates ---

func (a *Adapter) ListTemplates(ctx context.Context, opts adapter.ListOptions) ([]*adapter.Template, error) {
	// OpenSandbox doesn't have a template concept, return empty list
	// Templates are mapped to container images
	return []*adapter.Template{}, nil
}

func (a *Adapter) GetTemplate(ctx context.Context, templateID string) (*adapter.Template, error) {
	// OpenSandbox doesn't have a template concept
	// Return a synthetic template based on the image ID
	return &adapter.Template{
		TemplateID: templateID,
		Name:       templateID,
		CreatedAt:  time.Now(),
	}, nil
}

// --- Template Create/Delete ---

func (a *Adapter) CreateTemplate(_ context.Context, _ *adapter.CreateTemplateRequest) (*adapter.TemplateBuild, error) {
	return nil, fmt.Errorf("create template not supported by opensandbox backend")
}

func (a *Adapter) DeleteTemplate(_ context.Context, _ string) error {
	return fmt.Errorf("delete template not supported by opensandbox backend")
}

// --- Template Builds ---

func (a *Adapter) TriggerBuild(_ context.Context, _ string, _ *adapter.BuildRequest) (*adapter.TemplateBuild, error) {
	return nil, fmt.Errorf("trigger build not supported by opensandbox backend")
}

func (a *Adapter) GetBuildStatus(_ context.Context, _, _ string) (*adapter.BuildStatus, error) {
	return nil, fmt.Errorf("get build status not supported by opensandbox backend")
}

// --- Template Aliases ---

func (a *Adapter) CreateAlias(_ context.Context, _ string, _ string) error {
	return fmt.Errorf("create alias not supported by opensandbox backend")
}

func (a *Adapter) DeleteAlias(_ context.Context, _, _ string) error {
	return fmt.Errorf("delete alias not supported by opensandbox backend")
}

// --- Warm Pools ---

func (a *Adapter) ListWarmPools(_ context.Context) ([]*adapter.WarmPool, error) {
	return []*adapter.WarmPool{}, nil
}

func (a *Adapter) CreateWarmPool(_ context.Context, _ *adapter.WarmPoolCreateRequest) (*adapter.WarmPool, error) {
	return nil, fmt.Errorf("create warm pool not supported by opensandbox backend")
}

func (a *Adapter) GetWarmPool(_ context.Context, _ string) (*adapter.WarmPool, error) {
	return nil, fmt.Errorf("get warm pool not supported by opensandbox backend")
}

func (a *Adapter) DeleteWarmPool(_ context.Context, _ string) error {
	return fmt.Errorf("delete warm pool not supported by opensandbox backend")
}

func (a *Adapter) UpdateWarmPoolSize(_ context.Context, _ string, _ int) error {
	return fmt.Errorf("update warm pool size not supported by opensandbox backend")
}

// --- Processes ---

func (a *Adapter) ListProcesses(ctx context.Context, sandboxID string) ([]*adapter.ProcessInfo, error) {
	result, err := a.RunCommand(ctx, sandboxID, &adapter.CommandRequest{Command: "ps aux --no-headers"})
	if err != nil {
		return nil, fmt.Errorf("listing processes: %w", err)
	}
	var processes []*adapter.ProcessInfo
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Parse ps aux output: USER PID %CPU %MEM VSZ RSS TTY STAT START TIME COMMAND...
		fields := strings.Fields(line)
		if len(fields) < 11 {
			continue
		}
		// PID is the second field
		pidStr := fields[1]
		pid := 0
		if _, err := fmt.Sscanf(pidStr, "%d", &pid); err != nil {
			continue // Skip lines with invalid PID
		}

		// Command is everything after the 10th field
		command := strings.Join(fields[10:], " ")

		processes = append(processes, &adapter.ProcessInfo{
			ProcessID: pidStr,
			Command:   command,
			PID:       pid,
			Status:    fields[7], // STAT field
			StartedAt: time.Now(),
		})
	}
	return processes, nil
}

func (a *Adapter) KillProcess(ctx context.Context, sandboxID, processID string) error {
	// processID should be a real PID (from ListProcesses)
	// Validate it's a pure number to prevent shell injection
	var pid int
	var extra string
	n, err := fmt.Sscanf(processID, "%d%s", &pid, &extra)
	if n != 1 || (err != nil && err != io.EOF) {
		return fmt.Errorf("invalid process ID %q: must be a numeric PID", processID)
	}
	_, err = a.RunCommand(ctx, sandboxID, &adapter.CommandRequest{
		Command: fmt.Sprintf("kill -9 %d", pid),
	})
	return err
}

func (a *Adapter) SendStdin(_ context.Context, _, _ string, _ string) error {
	return fmt.Errorf("send stdin not supported by opensandbox backend")
}

// --- Snapshots ---

func (a *Adapter) CreateSnapshot(_ context.Context, _ string, _ *adapter.SnapshotRequest) (*adapter.Snapshot, error) {
	return nil, fmt.Errorf("create snapshot not supported by opensandbox backend")
}

func (a *Adapter) ListSnapshots(_ context.Context, _ string) ([]*adapter.Snapshot, error) {
	return []*adapter.Snapshot{}, nil
}

// --- Ports ---

// ListPorts returns the list of tracked ports for a sandbox.
// OpenSandbox does not provide a native API to list all open ports,
// so this method returns ports that have been accessed via GetPortURL.
func (a *Adapter) ListPorts(_ context.Context, sandboxID string) ([]*adapter.PortInfo, error) {
	a.portTrackerMu.RLock()
	defer a.portTrackerMu.RUnlock()

	ports, ok := a.portTracker[sandboxID]
	if !ok {
		return []*adapter.PortInfo{}, nil
	}

	result := make([]*adapter.PortInfo, 0, len(ports))
	for port, ready := range ports {
		result = append(result, &adapter.PortInfo{
			Port:  port,
			Ready: ready,
		})
	}
	return result, nil
}

// GetPortURL returns the URL for accessing a specific port in a sandbox.
// The port is tracked for subsequent ListPorts calls.
func (a *Adapter) GetPortURL(ctx context.Context, sandboxID string, port int) (string, error) {
	var ep *opensandbox.Endpoint
	var err error

	if a.useSignedEndpoint {
		expires := time.Now().Add(1 * time.Hour).Unix()
		ep, err = a.lifecycle.GetSignedEndpoint(ctx, sandboxID, port, expires)
	} else {
		useProxy := true
		ep, err = a.lifecycle.GetEndpoint(ctx, sandboxID, port, &useProxy)
	}
	if err != nil {
		return "", fmt.Errorf("getting endpoint for port %d in sandbox %q: %w", port, sandboxID, err)
	}

	url := ep.Endpoint
	if !strings.HasPrefix(url, "http") {
		url = "http://" + url
	}

	// Store server-returned headers for later use.
	if len(ep.Headers) > 0 {
		a.endpointHeaders.Set(fmt.Sprintf("%s-%d", sandboxID, port), ep.Headers)
	}

	// Track the port for ListPorts.
	a.portTrackerMu.Lock()
	if a.portTracker[sandboxID] == nil {
		a.portTracker[sandboxID] = make(map[int]bool)
	}
	a.portTracker[sandboxID][port] = true
	a.portTrackerMu.Unlock()

	return url, nil
}

// --- Access Token ---

// GetAccessToken returns a scoped access token for the sandbox.
// Behavior depends on useSignedEndpoint:
//   - true: calls GetSignedEndpoint to obtain a server-signed token (OSEP-0011)
//   - false: generates a random envd_{sandboxID}_{32-hex} token
//
// Tokens are cached with 1h TTL and reused until expiry.
func (a *Adapter) GetAccessToken(ctx context.Context, sandboxID string) (*adapter.AccessToken, error) {
	// Check cache for existing token.
	if cached, ok := a.tokenCache.Get(sandboxID); ok {
		if tokenStr, ok := cached.(string); ok {
			return &adapter.AccessToken{
				Token:     tokenStr,
				ExpiresAt: time.Now().Add(1 * time.Hour),
			}, nil
		}
	}

	if a.useSignedEndpoint {
		return a.getSignedAccessToken(ctx, sandboxID)
	}
	return a.generateRandomToken(sandboxID)
}

// getSignedAccessToken obtains a server-signed access token via GetSignedEndpoint.
// The signed endpoint URL itself serves as the token (OSEP-0011).
// Server-returned headers (auth cookies, etc.) are stored in endpointHeaders cache.
func (a *Adapter) getSignedAccessToken(ctx context.Context, sandboxID string) (*adapter.AccessToken, error) {
	expires := time.Now().Add(1 * time.Hour).Unix()
	ep, err := a.lifecycle.GetSignedEndpoint(ctx, sandboxID, 49983, expires)
	if err != nil {
		return nil, fmt.Errorf("getting signed endpoint for sandbox %q: %w", sandboxID, err)
	}

	// The signed endpoint URL is the token.
	token := ep.Endpoint
	if !strings.HasPrefix(token, "http") {
		token = "http://" + token
	}

	// Store token.
	a.tokenCache.Set(sandboxID, token)

	// Store server-returned headers (may contain auth cookies/signatures).
	if len(ep.Headers) > 0 {
		a.endpointHeaders.Set(sandboxID, ep.Headers)
	}

	return &adapter.AccessToken{
		Token:     token,
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}, nil
}

// generateRandomToken creates a new random token in the format
// envd_{sandboxID}_{32-hex-random} and stores it in the token cache.
func (a *Adapter) generateRandomToken(sandboxID string) (*adapter.AccessToken, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generating token: %w", err)
	}
	token := fmt.Sprintf("envd_%s_%s", sandboxID, hex.EncodeToString(b))

	a.tokenCache.Set(sandboxID, token)

	return &adapter.AccessToken{
		Token:     token,
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}, nil
}

// ValidateAccessToken checks if the given token matches the cached token
// for the sandbox. Returns false if no token is cached or token doesn't match.
func (a *Adapter) ValidateAccessToken(_ context.Context, sandboxID, token string) (bool, error) {
	cached, ok := a.tokenCache.Get(sandboxID)
	if !ok {
		return false, nil
	}
	cachedToken, ok := cached.(string)
	if !ok {
		return false, nil
	}
	return cachedToken == token, nil
}

// --- Environment Variables ---

func (a *Adapter) SetEnvs(_ context.Context, _ string, _ map[string]string) error {
	return fmt.Errorf("set envs not supported by opensandbox backend")
}

// --- Logs ---

func (a *Adapter) GetLogs(_ context.Context, _ string) ([]*adapter.LogEntry, error) {
	return []*adapter.LogEntry{}, nil
}

// --- File Move ---

func (a *Adapter) MoveFile(ctx context.Context, sandboxID string, src, dst string) error {
	execClient, err := a.getOrCreateExecdClient(ctx, sandboxID)
	if err != nil {
		return err
	}
	return execClient.MoveFiles(ctx, opensandbox.MoveRequest{
		{Src: src, Dest: dst},
	})
}

// --- Template Tags ---

func (a *Adapter) CreateTag(_ context.Context, _ string, _ *adapter.TagRequest) (*adapter.Tag, error) {
	return nil, fmt.Errorf("create tag not supported by opensandbox backend")
}

func (a *Adapter) ListTags(_ context.Context, _ string) ([]*adapter.Tag, error) {
	return []*adapter.Tag{}, nil
}

func (a *Adapter) DeleteTag(_ context.Context, _ string, _ string) error {
	return fmt.Errorf("delete tag not supported by opensandbox backend")
}

// --- envd Data Plane ---

// GetEnvdEndpoint returns the envd endpoint for a sandbox and the access
// token the SDK must present. The sandbox container must have envd running
// on port 49983.
//
// Behavior depends on useSignedEndpoint:
//   - true: calls GetSignedEndpoint to obtain a server-signed endpoint URL
//   - false: calls GetEndpoint via server proxy route
//
// In both modes, server-returned Endpoint.Headers (auth cookies, trace IDs,
// etc.) are stored in the endpointHeaders cache for later use by ExecdClient.
// The access token is retrieved from the token cache (generated on demand
// via GetAccessToken).
func (a *Adapter) GetEnvdEndpoint(ctx context.Context, sandboxID string) (string, string, error) {
	var ep *opensandbox.Endpoint
	var err error

	if a.useSignedEndpoint {
		expires := time.Now().Add(1 * time.Hour).Unix()
		ep, err = a.lifecycle.GetSignedEndpoint(ctx, sandboxID, 49983, expires)
	} else {
		useProxy := true
		ep, err = a.lifecycle.GetEndpoint(ctx, sandboxID, 49983, &useProxy)
	}
	if err != nil {
		return "", "", fmt.Errorf("getting envd endpoint for sandbox %q: %w", sandboxID, err)
	}

	envdURL := ep.Endpoint
	if !strings.HasPrefix(envdURL, "http") {
		envdURL = "http://" + envdURL
	}

	// Store server-returned headers for later use by ExecdClient.
	if len(ep.Headers) > 0 {
		a.endpointHeaders.Set(sandboxID, ep.Headers)
	}

	// Return the cached access token.
	token := ""
	if cached, ok := a.tokenCache.Get(sandboxID); ok {
		if tokenStr, ok := cached.(string); ok {
			token = tokenStr
		}
	}

	return envdURL, token, nil
}

// --- Helpers ---

func mapState(state opensandbox.SandboxState) adapter.SandboxStatus {
	switch state {
	case opensandbox.StateRunning:
		return adapter.SandboxStatusRunning
	case opensandbox.StatePaused:
		return adapter.SandboxStatusPaused
	case opensandbox.StateTerminated:
		return adapter.SandboxStatusStopped
	default:
		return adapter.SandboxStatusStarting
	}
}

func wrapCodeInCommand(code string, language string) string {
	switch strings.ToLower(language) {
	case "python", "python3", "":
		return fmt.Sprintf("python3 -c %q", code)
	case "javascript", "node":
		return fmt.Sprintf("node -e %q", code)
	case "bash", "sh":
		return code
	default:
		return fmt.Sprintf("%s -c %q", language, code)
	}
}

// extractText extracts the "text" field from NDJSON SSE data.
// The OpenSandbox execd server sends NDJSON events like:
//
//	{"type":"stdout","text":"hello\n","timestamp":123}
//
// The SDK's streamSSE puts the full JSON line in StreamEvent.Data.
// This helper extracts just the text content. If the data is not JSON
// or has no "text" field, it returns the raw data unchanged.
func extractText(data string) string {
	if len(data) == 0 || data[0] != '{' {
		return data
	}
	var ev struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return data
	}
	if ev.Text != "" {
		return ev.Text
	}
	return data
}
