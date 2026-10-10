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

package opensandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"sync"
	"time"

	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/adapter/util"
	"github.com/e2bgateway/e2bgateway/internal/cache"
)

const (
	defaultLanguage  = "python"
	streamTypeStdout = "stdout"
	streamTypeStderr = "stderr"
)

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

	// registry provides access to the sandbox→backend mapping.
	registry *adapter.Registry

	// templates is the pluggable store for template, build, alias, and tag
	// metadata. OpenSandbox uses container images natively but has no
	// built-in template concept; the gateway provides this abstraction
	// layer so E2B clients can manage templates uniformly across backends.
	//
	// The store is configurable: memory (default), redis, or etcd (future).
	templates TemplateStore
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
	// Registry provides access to the sandbox→backend mapping.
	Registry *adapter.Registry
	// TemplateStore is the pluggable persistence backend for templates, builds,
	// aliases, and tags. If nil, defaults to NewMemoryTemplateStore().
	TemplateStore TemplateStore
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

	templates := cfg.TemplateStore
	if templates == nil {
		templates = NewMemoryTemplateStore()
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
		registry:          cfg.Registry,
		templates:         templates,
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
			case opensandbox.StatePending, opensandbox.StatePausing, opensandbox.StatePaused, opensandbox.StateStopping:
				// Still transitioning; continue polling.
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
			maps.Copy(mergedHeaders, headers)
		}
	}
	maps.Copy(mergedHeaders, ep.Headers)
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
	// Resolve template ID → image URI through the following precedence:
	// 1. Static templateToImage map (config-level)
	// 2. Gateway-managed template store (CreateTemplate)
	// 3. Alias → templateID → image URI resolution
	// 4. Fall back to using req.TemplateID directly as image URI
	imageURI := req.TemplateID
	if mapped, ok := a.templateToImage[req.TemplateID]; ok {
		imageURI = mapped
	} else if a.templates != nil {
		// Try direct template lookup.
		if entry, err := a.templates.GetTemplate(ctx, req.TemplateID); err == nil {
			imageURI = entry.ImageURI
		} else if !errors.Is(err, ErrTemplateNotFound) {
			return nil, fmt.Errorf("looking up template %q: %w", req.TemplateID, err)
		}

		// Check alias → template resolution.
		if imageURI == req.TemplateID {
			if resolvedID, err := a.templates.ResolveAlias(ctx, req.TemplateID); err == nil {
				if entry, err := a.templates.GetTemplate(ctx, resolvedID); err == nil {
					imageURI = entry.ImageURI
				}
			}
		}
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
		Env:            req.Envs,
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

	// Register sandbox in the sandbox→backend mapping
	if a.registry != nil {
		a.registry.SandboxBackend().Set(sbx.ID, a.name)
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
	// Unregister sandbox from the sandbox→backend mapping
	if a.registry != nil {
		a.registry.SandboxBackend().Delete(sandboxID)
	}
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
		Command: util.WrapCodeInCommand(req.Code, lang),
		Timeout: 30000, // 30 seconds default
	}, func(event opensandbox.StreamEvent) error {
		switch event.Event {
		case streamTypeStdout:
			stdout.WriteString(extractText(event.Data))
		case streamTypeStderr:
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
		Command: util.WrapCodeInCommand(req.Code, lang),
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
		Data: map[string]any{"exitCode": 0},
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
		case streamTypeStdout:
			stdout.WriteString(extractText(event.Data))
		case streamTypeStderr:
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

// ListTemplates returns all gateway-managed templates sorted by creation
// time (oldest first) for deterministic pagination.
func (a *Adapter) ListTemplates(ctx context.Context, opts adapter.ListOptions) ([]*adapter.Template, error) {
	entries, err := a.templates.ListTemplates(ctx)
	if err != nil {
		return nil, err
	}

	allTemplates := make([]*adapter.Template, 0, len(entries))
	for _, entry := range entries {
		allTemplates = append(allTemplates, entryToTemplate(entry))
	}

	// Apply client-side pagination.
	start := max(opts.Offset, 0)
	if start >= len(allTemplates) {
		return []*adapter.Template{}, nil
	}
	end := len(allTemplates)
	if opts.Limit > 0 && start+opts.Limit < end {
		end = start + opts.Limit
	}
	return allTemplates[start:end], nil
}

// GetTemplate retrieves a gateway-managed template by ID. If the template is
// not in the managed store, a synthetic template is returned using the
// templateID as the image URI (backward-compatible with pre-existing behavior
// where any string could be used as a template/image).
func (a *Adapter) GetTemplate(ctx context.Context, templateID string) (*adapter.Template, error) {
	entry, err := a.templates.GetTemplate(ctx, templateID)
	if err == nil {
		return entryToTemplate(entry), nil
	}
	if !errors.Is(err, ErrTemplateNotFound) {
		return nil, err
	}
	// Synthetic template for backward compatibility.
	return &adapter.Template{
		TemplateID: templateID,
		Name:       templateID,
		CreatedAt:  time.Now(),
	}, nil
}

// --- Template Create/Delete ---

// CreateTemplate creates a new gateway-managed template. Since OpenSandbox
// uses container images directly (no build pipeline), the template is stored
// and becomes ready synchronously. The image URI is derived from the
// Dockerfile's last FROM directive when available, or defaults to
// "python:3.11-slim".
func (a *Adapter) CreateTemplate(ctx context.Context, req *adapter.CreateTemplateRequest) (*adapter.TemplateBuild, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("template name is required")
	}

	templateID := util.MustGenerateTemplateID(req.Name)
	buildID := "build-" + util.MustGenerateE2BID()

	// Resolve image URI from Dockerfile's last FROM directive, or use default.
	imageURI := "python:3.11-slim"
	if req.Dockerfile != "" {
		if from := util.ParseDockerfileFrom(req.Dockerfile); from != "" {
			imageURI = from
		}
	}

	entry := &TemplateEntry{
		TemplateID: templateID,
		Name:       req.Name,
		ImageURI:   imageURI,
		Dockerfile: req.Dockerfile,
		StartCmd:   req.StartCmd,
		CPUCount:   req.CPUCount,
		MemoryMB:   req.MemoryMB,
		BuildID:    buildID,
		CreatedAt:  time.Now(),
	}

	if err := a.templates.CreateTemplate(ctx, entry); err != nil {
		return nil, err
	}
	if err := a.templates.SaveBuild(ctx, templateID, &adapter.BuildStatus{
		BuildID: buildID,
		Status:  adapter.BuildStatusReady,
	}); err != nil {
		return nil, err
	}

	return &adapter.TemplateBuild{
		TemplateID: templateID,
		BuildID:    buildID,
		Status:     adapter.BuildStatusReady,
	}, nil
}

// DeleteTemplate removes a gateway-managed template and cleans up associated
// aliases, tags, and builds.
func (a *Adapter) DeleteTemplate(ctx context.Context, templateID string) error {
	// Cascade cleanup: aliases, tags, builds first.
	_ = a.templates.DeleteAliases(ctx, templateID)
	_ = a.templates.DeleteTags(ctx, templateID)
	_ = a.templates.DeleteBuilds(ctx, templateID)
	return a.templates.DeleteTemplate(ctx, templateID)
}

// --- Template Builds ---

// TriggerBuild generates a new build for an existing template. Since
// OpenSandbox doesn't have a native build pipeline, builds complete
// synchronously and the new build ID is recorded.
func (a *Adapter) TriggerBuild(ctx context.Context, templateID string, req *adapter.BuildRequest) (*adapter.TemplateBuild, error) {
	buildID := "build-" + util.MustGenerateE2BID()

	// Update the template's Dockerfile/startCmd/imageURI and current buildID.
	err := a.templates.UpdateTemplate(ctx, templateID, func(entry *TemplateEntry) error {
		entry.BuildID = buildID
		if req.Dockerfile != "" {
			entry.Dockerfile = req.Dockerfile
			if from := util.ParseDockerfileFrom(req.Dockerfile); from != "" {
				entry.ImageURI = from
			}
		}
		if req.StartCmd != "" {
			entry.StartCmd = req.StartCmd
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if err := a.templates.SaveBuild(ctx, templateID, &adapter.BuildStatus{
		BuildID: buildID,
		Status:  adapter.BuildStatusReady,
	}); err != nil {
		return nil, err
	}

	return &adapter.TemplateBuild{
		TemplateID: templateID,
		BuildID:    buildID,
		Status:     adapter.BuildStatusReady,
	}, nil
}

// GetBuildStatus returns the status of a template build. Since builds
// complete synchronously in OpenSandbox, this always returns "ready" for
// known builds.
func (a *Adapter) GetBuildStatus(ctx context.Context, _, buildID string) (*adapter.BuildStatus, error) {
	return a.templates.GetBuild(ctx, buildID)
}

// --- Template Aliases ---

// CreateAlias associates an alias with a gateway-managed template.
func (a *Adapter) CreateAlias(ctx context.Context, templateID string, alias string) error {
	if alias == "" {
		return fmt.Errorf("alias is required")
	}
	// Verify template exists (the store implementation handles TOCTOU safety).
	if _, err := a.templates.GetTemplate(ctx, templateID); err != nil {
		return fmt.Errorf("template %q not found", templateID)
	}
	return a.templates.AddAlias(ctx, templateID, alias)
}

// DeleteAlias removes an alias from a gateway-managed template.
func (a *Adapter) DeleteAlias(ctx context.Context, templateID, alias string) error {
	return a.templates.RemoveAlias(ctx, templateID, alias)
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
	for line := range strings.SplitSeq(result.Stdout, "\n") {
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
	if n != 1 || (err != nil && !errors.Is(err, io.EOF)) {
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

// SetEnvs writes environment variables into the sandbox by appending them to
// /etc/environment via the execd command channel. Each RunCommand spawns a new
// shell, so export does not persist; new shells pick the variables up from
// /etc/environment (cross-session).
func (a *Adapter) SetEnvs(ctx context.Context, sandboxID string, envs map[string]string) error {
	if len(envs) == 0 {
		return nil
	}

	execClient, err := a.getOrCreateExecdClient(ctx, sandboxID)
	if err != nil {
		return err
	}

	_, err = a.runCommandQuiet(ctx, execClient, buildSetEnvsCommand(envs))
	return err
}

// runCommandQuiet executes a command via the execd client and discards output.
func (a *Adapter) runCommandQuiet(ctx context.Context, execClient *opensandbox.ExecdClient, command string) (*adapter.CommandResult, error) {
	var stdout, stderr strings.Builder
	err := execClient.RunCommand(ctx, opensandbox.RunCommandRequest{
		Command: command,
		Timeout: 30000, // 30 seconds
	}, func(event opensandbox.StreamEvent) error {
		switch event.Event {
		case streamTypeStdout:
			stdout.WriteString(extractText(event.Data))
		case streamTypeStderr:
			stderr.WriteString(extractText(event.Data))
		}
		return nil
	})
	if err != nil {
		return &adapter.CommandResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: 1}, err
	}
	return &adapter.CommandResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: 0}, nil
}

// buildSetEnvsCommand builds the shell command that appends environment
// variables to /etc/environment. Each line is formatted KEY="value" (quoted to
// handle spaces/special chars) and the whole content is shell-quoted to
// prevent injection.
func buildSetEnvsCommand(envs map[string]string) string {
	envLines := make([]string, 0, len(envs))
	for k, v := range envs {
		envLines = append(envLines, fmt.Sprintf("%s=%q", k, v))
	}
	content := strings.Join(envLines, "\n") + "\n"
	return fmt.Sprintf("echo %s >> /etc/environment", util.ShellQuote(content))
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

// CreateTag creates a new tag on a gateway-managed template. Tags point to
// specific build IDs (similar to git tags pointing to commits).
func (a *Adapter) CreateTag(ctx context.Context, templateID string, req *adapter.TagRequest) (*adapter.Tag, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("tag name is required")
	}
	// Verify template exists.
	if _, err := a.templates.GetTemplate(ctx, templateID); err != nil {
		return nil, fmt.Errorf("template %q not found", templateID)
	}

	tag := &adapter.Tag{
		Name:       req.Name,
		TemplateID: templateID,
		BuildID:    req.BuildID,
		CreatedAt:  time.Now(),
	}
	if err := a.templates.SaveTag(ctx, templateID, tag); err != nil {
		return nil, err
	}
	return tag, nil
}

// ListTags returns all tags for a gateway-managed template.
func (a *Adapter) ListTags(ctx context.Context, templateID string) ([]*adapter.Tag, error) {
	return a.templates.ListTags(ctx, templateID)
}

// DeleteTag removes a tag from a gateway-managed template.
func (a *Adapter) DeleteTag(ctx context.Context, templateID, tagName string) error {
	return a.templates.DeleteTag(ctx, templateID, tagName)
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
	case opensandbox.StatePending, opensandbox.StatePausing, opensandbox.StateStopping:
		return adapter.SandboxStatusStarting
	case opensandbox.StateFailed:
		return adapter.SandboxStatusStopped
	default:
		return adapter.SandboxStatusStarting
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

// --- Template Helpers ---

// entryToTemplate converts a TemplateEntry to the domain Template.
func entryToTemplate(entry *TemplateEntry) *adapter.Template {
	return &adapter.Template{
		TemplateID: entry.TemplateID,
		Name:       entry.Name,
		CPUCount:   entry.CPUCount,
		MemoryMB:   entry.MemoryMB,
		BuildID:    entry.BuildID,
		CreatedAt:  entry.CreatedAt,
		Metadata:   entry.Metadata,
	}
}
