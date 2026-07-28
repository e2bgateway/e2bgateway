// Package mock provides a mock sandbox adapter for testing.
package mock

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
)

// Adapter is a mock sandbox adapter that stores state in memory.
type Adapter struct {
	mu         sync.RWMutex
	sandboxes  map[string]*adapter.Sandbox
	templates  map[string]*adapter.Template
	warmPools  map[string]*adapter.WarmPool
	processes  map[string][]*adapter.ProcessInfo // sandboxID -> processes
	snapshots  map[string][]*adapter.Snapshot    // sandboxID -> snapshots
	builds     map[string]*adapter.BuildStatus   // buildID -> status
	aliases    map[string][]string               // templateID -> aliases
	portCount  map[string]int                    // sandboxID -> port count
	tags       map[string][]*adapter.Tag         // templateID -> tags
	files      map[string]map[string][]byte      // sandboxID -> path -> content
}

// New creates a new mock adapter.
func New() *Adapter {
	return &Adapter{
		sandboxes: make(map[string]*adapter.Sandbox),
		templates: map[string]*adapter.Template{
			"base": {
				TemplateID:  "base",
				Name:        "base",
				Description: "Base sandbox template",
				CPUCount:    2,
				MemoryMB:    512,
				Public:      true,
				CreatedAt:   time.Now(),
			},
			"code-interpreter": {
				TemplateID:  "code-interpreter",
				Name:        "code-interpreter",
				Description: "Python code interpreter template",
				CPUCount:    2,
				MemoryMB:    1024,
				Public:      true,
				CreatedAt:   time.Now(),
			},
		},
		warmPools: make(map[string]*adapter.WarmPool),
		processes: make(map[string][]*adapter.ProcessInfo),
		snapshots: make(map[string][]*adapter.Snapshot),
		builds:    make(map[string]*adapter.BuildStatus),
		aliases:   make(map[string][]string),
		portCount: make(map[string]int),
		tags:      make(map[string][]*adapter.Tag),
		files:     make(map[string]map[string][]byte),
	}
}

func (a *Adapter) Name() string { return "mock" }

func (a *Adapter) HealthCheck(_ context.Context) error { return nil }

func (a *Adapter) CreateSandbox(_ context.Context, req *adapter.CreateSandboxRequest) (*adapter.Sandbox, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	id := fmt.Sprintf("mock-%d", len(a.sandboxes)+1)
	sbx := &adapter.Sandbox{
		SandboxID:  id,
		TemplateID: req.TemplateID,
		Alias:      req.Alias,
		StartedAt:  time.Now(),
		EndAt:      time.Now().Add(5 * time.Minute),
		Status:     adapter.SandboxStatusRunning,
		Metadata:   req.Metadata,
		Backend:    "mock",
	}
	if req.Timeout > 0 {
		sbx.EndAt = time.Now().Add(time.Duration(req.Timeout) * time.Second)
	}
	a.sandboxes[id] = sbx
	return sbx, nil
}

func (a *Adapter) ListSandboxes(_ context.Context, _ adapter.ListOptions) ([]*adapter.Sandbox, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	result := make([]*adapter.Sandbox, 0, len(a.sandboxes))
	for _, s := range a.sandboxes {
		result = append(result, s)
	}
	return result, nil
}

func (a *Adapter) GetSandbox(_ context.Context, sandboxID string) (*adapter.Sandbox, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	s, ok := a.sandboxes[sandboxID]
	if !ok {
		return nil, fmt.Errorf("sandbox %q not found", sandboxID)
	}
	return s, nil
}

func (a *Adapter) KillSandbox(_ context.Context, sandboxID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.sandboxes[sandboxID]; !ok {
		return fmt.Errorf("sandbox %q not found", sandboxID)
	}
	delete(a.sandboxes, sandboxID)
	return nil
}

func (a *Adapter) PauseSandbox(_ context.Context, sandboxID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	s, ok := a.sandboxes[sandboxID]
	if !ok {
		return fmt.Errorf("sandbox %q not found", sandboxID)
	}
	s.Status = adapter.SandboxStatusPaused
	return nil
}

func (a *Adapter) ResumeSandbox(_ context.Context, sandboxID string) (*adapter.Sandbox, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	s, ok := a.sandboxes[sandboxID]
	if !ok {
		return nil, fmt.Errorf("sandbox %q not found", sandboxID)
	}
	s.Status = adapter.SandboxStatusRunning
	return s, nil
}

func (a *Adapter) SetTimeout(_ context.Context, sandboxID string, timeout time.Duration) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	s, ok := a.sandboxes[sandboxID]
	if !ok {
		return fmt.Errorf("sandbox %q not found", sandboxID)
	}
	s.EndAt = time.Now().Add(timeout)
	return nil
}

func (a *Adapter) ExecuteCode(_ context.Context, sandboxID string, req *adapter.CodeExecutionRequest) (*adapter.CodeExecutionResult, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if _, ok := a.sandboxes[sandboxID]; !ok {
		return nil, fmt.Errorf("sandbox %q not found", sandboxID)
	}
	return &adapter.CodeExecutionResult{
		Stdout:   fmt.Sprintf("[mock] executed: %s\n", req.Code),
		ExitCode: 0,
		Duration: 100 * time.Millisecond,
	}, nil
}

func (a *Adapter) ExecuteCodeStream(_ context.Context, _ string, _ *adapter.CodeExecutionRequest, _ adapter.CodeStream) error {
	return fmt.Errorf("not implemented")
}

func (a *Adapter) RunCommand(_ context.Context, sandboxID string, req *adapter.CommandRequest) (*adapter.CommandResult, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if _, ok := a.sandboxes[sandboxID]; !ok {
		return nil, fmt.Errorf("sandbox %q not found", sandboxID)
	}
	return &adapter.CommandResult{
		Stdout:   fmt.Sprintf("[mock] ran: %s\n", req.Command),
		ExitCode: 0,
	}, nil
}

func (a *Adapter) WriteFile(_ context.Context, sandboxID string, req *adapter.FileWriteRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.sandboxes[sandboxID]; !ok {
		return fmt.Errorf("sandbox %q not found", sandboxID)
	}
	if a.files[sandboxID] == nil {
		a.files[sandboxID] = make(map[string][]byte)
	}
	a.files[sandboxID][req.Path] = req.Content
	return nil
}

func (a *Adapter) ReadFile(_ context.Context, sandboxID string, path string) (*adapter.FileContent, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.sandboxes[sandboxID]; !ok {
		return nil, fmt.Errorf("sandbox %q not found", sandboxID)
	}
	if files, ok := a.files[sandboxID]; ok {
		if content, ok := files[path]; ok {
			return &adapter.FileContent{Path: path, Content: content, Size: int64(len(content))}, nil
		}
	}
	return &adapter.FileContent{Path: path, Content: []byte("[mock file content]"), Size: 19}, nil
}

func (a *Adapter) UploadFile(_ context.Context, sandboxID string, req *adapter.FileUploadRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.sandboxes[sandboxID]; !ok {
		return fmt.Errorf("sandbox %q not found", sandboxID)
	}
	if a.files[sandboxID] == nil {
		a.files[sandboxID] = make(map[string][]byte)
	}
	data, err := io.ReadAll(req.Reader)
	if err != nil {
		return fmt.Errorf("reading upload data: %w", err)
	}
	a.files[sandboxID][req.Path] = data
	return nil
}

func (a *Adapter) DownloadFile(_ context.Context, sandboxID string, path string) (io.ReadCloser, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.sandboxes[sandboxID]; !ok {
		return nil, fmt.Errorf("sandbox %q not found", sandboxID)
	}
	if files, ok := a.files[sandboxID]; ok {
		if content, ok := files[path]; ok {
			return io.NopCloser(bytes.NewReader(content)), nil
		}
	}
	return nil, fmt.Errorf("file %q not found", path)
}

func (a *Adapter) ListFiles(_ context.Context, sandboxID string, path string) ([]adapter.FileInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.sandboxes[sandboxID]; !ok {
		return nil, fmt.Errorf("sandbox %q not found", sandboxID)
	}
	var result []adapter.FileInfo
	if files, ok := a.files[sandboxID]; ok {
		for p, content := range files {
			// Match files under the requested path
			if path == "/" || path == "" || strings.HasPrefix(p, path) {
				name := p
				if idx := strings.LastIndex(p, "/"); idx >= 0 {
					name = p[idx+1:]
				}
				result = append(result, adapter.FileInfo{
					Name:    name,
					Path:    p,
					Size:    int64(len(content)),
					IsDir:   false,
					ModTime: time.Now(),
				})
			}
		}
	}
	return result, nil
}

func (a *Adapter) MakeDir(_ context.Context, sandboxID string, _ string) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.sandboxes[sandboxID]; !ok {
		return fmt.Errorf("sandbox %q not found", sandboxID)
	}
	return nil
}

func (a *Adapter) RemoveFile(_ context.Context, sandboxID string, _ string) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.sandboxes[sandboxID]; !ok {
		return fmt.Errorf("sandbox %q not found", sandboxID)
	}
	return nil
}

func (a *Adapter) ListTemplates(_ context.Context, _ adapter.ListOptions) ([]*adapter.Template, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	result := make([]*adapter.Template, 0, len(a.templates))
	for _, t := range a.templates {
		result = append(result, t)
	}
	return result, nil
}

func (a *Adapter) GetTemplate(_ context.Context, templateID string) (*adapter.Template, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	t, ok := a.templates[templateID]
	if !ok {
		return nil, fmt.Errorf("template %q not found", templateID)
	}
	return t, nil
}

func (a *Adapter) CreateTemplate(_ context.Context, req *adapter.CreateTemplateRequest) (*adapter.TemplateBuild, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	id := generateMockID()
	buildID := "build-" + id
	a.templates[id] = &adapter.Template{
		TemplateID:  id,
		Name:        req.Name,
		Description: "Custom template",
		CPUCount:    req.CPUCount,
		MemoryMB:    req.MemoryMB,
		CreatedAt:   time.Now(),
		BuildID:     buildID,
	}
	a.builds[buildID] = &adapter.BuildStatus{
		BuildID: buildID,
		Status:  "ready",
	}
	return &adapter.TemplateBuild{
		TemplateID: id,
		BuildID:    buildID,
		Status:     "ready",
	}, nil
}

func (a *Adapter) DeleteTemplate(_ context.Context, templateID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.templates[templateID]; !ok {
		return fmt.Errorf("template %q not found", templateID)
	}
	delete(a.templates, templateID)
	return nil
}

// --- Template Builds ---

func (a *Adapter) TriggerBuild(_ context.Context, templateID string, _ *adapter.BuildRequest) (*adapter.TemplateBuild, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.templates[templateID]; !ok {
		return nil, fmt.Errorf("template %q not found", templateID)
	}
	buildID := "build-" + generateMockID()
	a.builds[buildID] = &adapter.BuildStatus{
		BuildID: buildID,
		Status:  "ready",
	}
	return &adapter.TemplateBuild{
		TemplateID: templateID,
		BuildID:    buildID,
		Status:     "ready",
	}, nil
}

func (a *Adapter) GetBuildStatus(_ context.Context, _, buildID string) (*adapter.BuildStatus, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	bs, ok := a.builds[buildID]
	if !ok {
		return nil, fmt.Errorf("build %q not found", buildID)
	}
	return bs, nil
}

// --- Template Aliases ---

func (a *Adapter) CreateAlias(_ context.Context, templateID string, alias string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.templates[templateID]; !ok {
		return fmt.Errorf("template %q not found", templateID)
	}
	a.aliases[templateID] = append(a.aliases[templateID], alias)
	return nil
}

func (a *Adapter) DeleteAlias(_ context.Context, templateID, alias string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	aliases := a.aliases[templateID]
	for i, al := range aliases {
		if al == alias {
			a.aliases[templateID] = append(aliases[:i], aliases[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("alias %q not found for template %q", alias, templateID)
}

// --- Warm Pools ---

func (a *Adapter) ListWarmPools(_ context.Context) ([]*adapter.WarmPool, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	result := make([]*adapter.WarmPool, 0, len(a.warmPools))
	for _, wp := range a.warmPools {
		result = append(result, wp)
	}
	return result, nil
}

func (a *Adapter) CreateWarmPool(_ context.Context, req *adapter.WarmPoolCreateRequest) (*adapter.WarmPool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	id := req.WarmPoolID
	if id == "" {
		id = "wp-" + generateMockID()
	}
	wp := &adapter.WarmPool{
		WarmPoolID:  id,
		TemplateID:  req.TemplateID,
		TargetSize:  req.TargetSize,
		CurrentSize: 0,
		Status:      "active",
		CreatedAt:   time.Now(),
	}
	a.warmPools[id] = wp
	return wp, nil
}

func (a *Adapter) GetWarmPool(_ context.Context, warmPoolID string) (*adapter.WarmPool, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	wp, ok := a.warmPools[warmPoolID]
	if !ok {
		return nil, fmt.Errorf("warm pool %q not found", warmPoolID)
	}
	return wp, nil
}

func (a *Adapter) DeleteWarmPool(_ context.Context, warmPoolID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.warmPools[warmPoolID]; !ok {
		return fmt.Errorf("warm pool %q not found", warmPoolID)
	}
	delete(a.warmPools, warmPoolID)
	return nil
}

func (a *Adapter) UpdateWarmPoolSize(_ context.Context, warmPoolID string, targetSize int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	wp, ok := a.warmPools[warmPoolID]
	if !ok {
		return fmt.Errorf("warm pool %q not found", warmPoolID)
	}
	wp.TargetSize = targetSize
	return nil
}

// --- Processes ---

func (a *Adapter) ListProcesses(_ context.Context, sandboxID string) ([]*adapter.ProcessInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.sandboxes[sandboxID]; !ok {
		return nil, fmt.Errorf("sandbox %q not found", sandboxID)
	}
	return a.processes[sandboxID], nil
}

func (a *Adapter) KillProcess(_ context.Context, sandboxID, processID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.sandboxes[sandboxID]; !ok {
		return fmt.Errorf("sandbox %q not found", sandboxID)
	}
	procs := a.processes[sandboxID]
	for i, p := range procs {
		if p.ProcessID == processID {
			a.processes[sandboxID] = append(procs[:i], procs[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("process %q not found", processID)
}

func (a *Adapter) SendStdin(_ context.Context, sandboxID, processID string, _ string) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.sandboxes[sandboxID]; !ok {
		return fmt.Errorf("sandbox %q not found", sandboxID)
	}
	for _, p := range a.processes[sandboxID] {
		if p.ProcessID == processID {
			return nil
		}
	}
	return fmt.Errorf("process %q not found", processID)
}

// --- Snapshots ---

func (a *Adapter) CreateSnapshot(_ context.Context, sandboxID string, req *adapter.SnapshotRequest) (*adapter.Snapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.sandboxes[sandboxID]; !ok {
		return nil, fmt.Errorf("sandbox %q not found", sandboxID)
	}
	snap := &adapter.Snapshot{
		SnapshotID:  "snap-" + generateMockID(),
		SandboxID:   sandboxID,
		Name:        req.Name,
		Description: req.Description,
		CreatedAt:   time.Now(),
		SizeMB:      10,
	}
	a.snapshots[sandboxID] = append(a.snapshots[sandboxID], snap)
	return snap, nil
}

func (a *Adapter) ListSnapshots(_ context.Context, sandboxID string) ([]*adapter.Snapshot, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.sandboxes[sandboxID]; !ok {
		return nil, fmt.Errorf("sandbox %q not found", sandboxID)
	}
	return a.snapshots[sandboxID], nil
}

// --- Ports ---

func (a *Adapter) ListPorts(_ context.Context, sandboxID string) ([]*adapter.PortInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.sandboxes[sandboxID]; !ok {
		return nil, fmt.Errorf("sandbox %q not found", sandboxID)
	}
	return []*adapter.PortInfo{
		{Port: 3000, Ready: true},
		{Port: 8080, Ready: true},
	}, nil
}

func (a *Adapter) GetPortURL(_ context.Context, sandboxID string, port int) (string, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.sandboxes[sandboxID]; !ok {
		return "", fmt.Errorf("sandbox %q not found", sandboxID)
	}
	return fmt.Sprintf("https://%s-%d.mock.e2b.dev", sandboxID, port), nil
}

// --- Access Token ---

func (a *Adapter) GetAccessToken(_ context.Context, sandboxID string) (*adapter.AccessToken, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.sandboxes[sandboxID]; !ok {
		return nil, fmt.Errorf("sandbox %q not found", sandboxID)
	}
	return &adapter.AccessToken{
		Token:     "mock-token-" + generateMockID(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}, nil
}

// --- Environment Variables ---

func (a *Adapter) SetEnvs(_ context.Context, sandboxID string, envs map[string]string) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.sandboxes[sandboxID]; !ok {
		return fmt.Errorf("sandbox %q not found", sandboxID)
	}
	// Mock: just validate the sandbox exists
	return nil
}

// --- Logs ---

func (a *Adapter) GetLogs(_ context.Context, sandboxID string) ([]*adapter.LogEntry, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.sandboxes[sandboxID]; !ok {
		return nil, fmt.Errorf("sandbox %q not found", sandboxID)
	}
	return []*adapter.LogEntry{
		{Timestamp: time.Now(), Message: "sandbox started", Level: "info", Source: "system"},
	}, nil
}

// --- File Move ---

func (a *Adapter) MoveFile(_ context.Context, sandboxID string, _, _ string) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.sandboxes[sandboxID]; !ok {
		return fmt.Errorf("sandbox %q not found", sandboxID)
	}
	return nil
}

// --- Template Tags ---

func (a *Adapter) CreateTag(_ context.Context, templateID string, req *adapter.TagRequest) (*adapter.Tag, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.templates[templateID]; !ok {
		return nil, fmt.Errorf("template %q not found", templateID)
	}
	tag := &adapter.Tag{
		Name:       req.Name,
		TemplateID: templateID,
		BuildID:    req.BuildID,
		CreatedAt:  time.Now(),
	}
	a.tags[templateID] = append(a.tags[templateID], tag)
	return tag, nil
}

func (a *Adapter) ListTags(_ context.Context, templateID string) ([]*adapter.Tag, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.templates[templateID]; !ok {
		return nil, fmt.Errorf("template %q not found", templateID)
	}
	return a.tags[templateID], nil
}

func (a *Adapter) DeleteTag(_ context.Context, templateID, tagName string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.templates[templateID]; !ok {
		return fmt.Errorf("template %q not found", templateID)
	}
	tags := a.tags[templateID]
	for i, t := range tags {
		if t.Name == tagName {
			a.tags[templateID] = append(tags[:i], tags[i+1:]...)
			return nil
		}
	}
	return nil
}

// --- Helpers ---

func generateMockID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
