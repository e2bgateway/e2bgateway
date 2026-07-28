// Package adapter defines the backend adapter interface and registry for E2BGateway.
package adapter

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/config"
)

// SandboxAdapter defines the contract for all sandbox backend implementations.
type SandboxAdapter interface {
	// Name returns the unique identifier for this adapter.
	Name() string

	// HealthCheck verifies connectivity and readiness of the backend.
	HealthCheck(ctx context.Context) error

	// --- Sandbox Lifecycle ---

	CreateSandbox(ctx context.Context, req *CreateSandboxRequest) (*Sandbox, error)
	ListSandboxes(ctx context.Context, opts ListOptions) ([]*Sandbox, error)
	GetSandbox(ctx context.Context, sandboxID string) (*Sandbox, error)
	KillSandbox(ctx context.Context, sandboxID string) error
	PauseSandbox(ctx context.Context, sandboxID string) error
	ResumeSandbox(ctx context.Context, sandboxID string) (*Sandbox, error)
	SetTimeout(ctx context.Context, sandboxID string, timeout time.Duration) error

	// --- Code Execution ---

	ExecuteCode(ctx context.Context, sandboxID string, req *CodeExecutionRequest) (*CodeExecutionResult, error)
	ExecuteCodeStream(ctx context.Context, sandboxID string, req *CodeExecutionRequest, stream CodeStream) error
	RunCommand(ctx context.Context, sandboxID string, req *CommandRequest) (*CommandResult, error)

	// --- Filesystem ---

	WriteFile(ctx context.Context, sandboxID string, req *FileWriteRequest) error
	ReadFile(ctx context.Context, sandboxID string, path string) (*FileContent, error)
	UploadFile(ctx context.Context, sandboxID string, req *FileUploadRequest) error
	DownloadFile(ctx context.Context, sandboxID string, path string) (io.ReadCloser, error)
	ListFiles(ctx context.Context, sandboxID string, path string) ([]FileInfo, error)
	MakeDir(ctx context.Context, sandboxID string, path string) error
	RemoveFile(ctx context.Context, sandboxID string, path string) error

	// --- Templates ---

	ListTemplates(ctx context.Context, opts ListOptions) ([]*Template, error)
	GetTemplate(ctx context.Context, templateID string) (*Template, error)
	CreateTemplate(ctx context.Context, req *CreateTemplateRequest) (*TemplateBuild, error)
	DeleteTemplate(ctx context.Context, templateID string) error

	// --- Template Builds ---

	TriggerBuild(ctx context.Context, templateID string, req *BuildRequest) (*TemplateBuild, error)
	GetBuildStatus(ctx context.Context, templateID, buildID string) (*BuildStatus, error)

	// --- Template Aliases ---

	CreateAlias(ctx context.Context, templateID string, alias string) error
	DeleteAlias(ctx context.Context, templateID, alias string) error

	// --- Warm Pools ---

	ListWarmPools(ctx context.Context) ([]*WarmPool, error)
	CreateWarmPool(ctx context.Context, req *WarmPoolCreateRequest) (*WarmPool, error)
	GetWarmPool(ctx context.Context, warmPoolID string) (*WarmPool, error)
	DeleteWarmPool(ctx context.Context, warmPoolID string) error
	UpdateWarmPoolSize(ctx context.Context, warmPoolID string, targetSize int) error

	// --- Processes ---

	ListProcesses(ctx context.Context, sandboxID string) ([]*ProcessInfo, error)
	KillProcess(ctx context.Context, sandboxID, processID string) error
	SendStdin(ctx context.Context, sandboxID, processID string, data string) error

	// --- Snapshots ---

	CreateSnapshot(ctx context.Context, sandboxID string, req *SnapshotRequest) (*Snapshot, error)
	ListSnapshots(ctx context.Context, sandboxID string) ([]*Snapshot, error)

	// --- Ports ---

	ListPorts(ctx context.Context, sandboxID string) ([]*PortInfo, error)
	GetPortURL(ctx context.Context, sandboxID string, port int) (string, error)

	// --- Access Token ---

	GetAccessToken(ctx context.Context, sandboxID string) (*AccessToken, error)

	// --- Environment Variables ---

	SetEnvs(ctx context.Context, sandboxID string, envs map[string]string) error

	// --- Logs ---

	GetLogs(ctx context.Context, sandboxID string) ([]*LogEntry, error)

	// --- File Move ---

	MoveFile(ctx context.Context, sandboxID string, src, dst string) error

	// --- Template Tags ---

	CreateTag(ctx context.Context, templateID string, req *TagRequest) (*Tag, error)
	ListTags(ctx context.Context, templateID string) ([]*Tag, error)
	DeleteTag(ctx context.Context, templateID, tagName string) error
}

// --- Domain Types ---

// Sandbox represents a running sandbox instance.
type Sandbox struct {
	SandboxID  string            `json:"sandboxID"`
	TemplateID string            `json:"templateID"`
	Alias      string            `json:"alias,omitempty"`
	StartedAt  time.Time         `json:"startedAt"`
	EndAt      time.Time         `json:"endAt"`
	Status     SandboxStatus     `json:"status"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	ClientID   string            `json:"clientID,omitempty"`
	Backend    string            `json:"backend"`
}

// SandboxStatus represents the lifecycle state of a sandbox.
type SandboxStatus string

const (
	SandboxStatusRunning  SandboxStatus = "running"
	SandboxStatusPaused   SandboxStatus = "paused"
	SandboxStatusStopped  SandboxStatus = "stopped"
	SandboxStatusError    SandboxStatus = "error"
	SandboxStatusStarting SandboxStatus = "starting"
)

// Template represents a sandbox template/blueprint.
type Template struct {
	TemplateID  string            `json:"templateID"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	CPUCount    int               `json:"cpuCount,omitempty"`
	MemoryMB    int               `json:"memoryMB,omitempty"`
	Public      bool              `json:"public,omitempty"`
	BuildID     string            `json:"buildID,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   time.Time         `json:"createdAt"`
}

// --- Request/Response Types ---

// CreateSandboxRequest is the input for creating a new sandbox.
type CreateSandboxRequest struct {
	TemplateID string            `json:"templateID"`
	Timeout    int               `json:"timeout,omitempty"` // seconds
	Alias      string            `json:"alias,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// ListOptions provides filtering/pagination for list operations.
type ListOptions struct {
	Limit  int               `json:"limit,omitempty"`
	Offset int               `json:"offset,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

// CodeExecutionRequest is the input for code execution.
type CodeExecutionRequest struct {
	Code     string            `json:"code"`
	Language string            `json:"language,omitempty"` // python, javascript
	EnvVars  map[string]string `json:"envVars,omitempty"`
	Cwd      string            `json:"cwd,omitempty"`
}

// CodeExecutionResult is the output of code execution.
type CodeExecutionResult struct {
	Stdout   string        `json:"stdout"`
	Stderr   string        `json:"stderr"`
	ExitCode int           `json:"exitCode"`
	Duration time.Duration `json:"duration"`
	Error    string        `json:"error,omitempty"`
}

// CodeStream is the interface for streaming code execution output.
type CodeStream interface {
	Send(msg *StreamMessage) error
	Close() error
}

// StreamMessage represents a streaming output message.
type StreamMessage struct {
	Type string      `json:"type"` // stdout, stderr, result, error
	Data interface{} `json:"data"`
}

// CommandRequest is the input for running a shell command.
type CommandRequest struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	EnvVars map[string]string `json:"envVars,omitempty"`
}

// CommandResult is the output of a command execution.
type CommandResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
	Error    string `json:"error,omitempty"`
}

// FileWriteRequest is the input for writing a file.
type FileWriteRequest struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
	Owner   string `json:"owner,omitempty"`
}

// FileContent represents file content read from a sandbox.
type FileContent struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
	Size    int64  `json:"size"`
}

// FileUploadRequest is the input for uploading a file.
type FileUploadRequest struct {
	Path   string        `json:"path"`
	Reader io.ReadCloser `json:"-"`
}

// FileInfo represents metadata about a file in a sandbox.
type FileInfo struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	IsDir   bool      `json:"isDir"`
	ModTime time.Time `json:"modTime"`
}

// --- Template Build Types ---

// CreateTemplateRequest is the input for creating a new template.
type CreateTemplateRequest struct {
	Name       string `json:"name"`
	Dockerfile string `json:"dockerfile,omitempty"`
	StartCmd   string `json:"startCmd,omitempty"`
	CPUCount   int    `json:"cpuCount,omitempty"`
	MemoryMB   int    `json:"memoryMB,omitempty"`
}

// TemplateBuild represents a template build in progress.
type TemplateBuild struct {
	TemplateID string `json:"templateID"`
	BuildID    string `json:"buildID"`
	Status     string `json:"status"` // "building", "ready", "error"
}

// BuildRequest is the input for triggering a template build.
type BuildRequest struct {
	Dockerfile string `json:"dockerfile,omitempty"`
	StartCmd   string `json:"startCmd,omitempty"`
}

// BuildStatus represents the status of a template build.
type BuildStatus struct {
	BuildID string `json:"buildID"`
	Status  string `json:"status"` // "building", "ready", "error"
	Logs    string `json:"logs,omitempty"`
	Error   string `json:"error,omitempty"`
}

// --- Warm Pool Types ---

// WarmPool represents a warm pool of pre-warmed sandbox instances.
type WarmPool struct {
	WarmPoolID  string    `json:"warmPoolID"`
	TemplateID  string    `json:"templateID"`
	TargetSize  int       `json:"targetSize"`
	CurrentSize int       `json:"currentSize"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
}

// WarmPoolCreateRequest is the input for creating a warm pool.
type WarmPoolCreateRequest struct {
	TemplateID string `json:"templateID"`
	TargetSize int    `json:"targetSize"`
	WarmPoolID string `json:"warmPoolID,omitempty"`
}

// --- Process Types ---

// ProcessInfo represents a running process in a sandbox.
type ProcessInfo struct {
	ProcessID string    `json:"processID"`
	Command   string    `json:"command"`
	PID       int       `json:"pid"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"startedAt"`
}

// --- Snapshot Types ---

// SnapshotRequest is the input for creating a snapshot.
type SnapshotRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Snapshot represents a sandbox snapshot.
type Snapshot struct {
	SnapshotID  string    `json:"snapshotID"`
	SandboxID   string    `json:"sandboxID"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	SizeMB      int       `json:"sizeMB,omitempty"`
}

// --- Port Types ---

// PortInfo represents an open port in a sandbox.
type PortInfo struct {
	Port  int  `json:"port"`
	Ready bool `json:"ready"`
}

// --- Access Token Types ---

// AccessToken represents a scoped access token for direct sandbox connections.
type AccessToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// --- Log Types ---

// LogEntry represents a single log entry from a sandbox.
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
	Level     string    `json:"level,omitempty"`
	Source    string    `json:"source,omitempty"`
}

// --- Tag Types ---

// TagRequest is the input for creating a template tag.
type TagRequest struct {
	Name    string `json:"name"`
	BuildID string `json:"buildID,omitempty"`
}

// Tag represents a template tag.
type Tag struct {
	Name       string    `json:"name"`
	TemplateID string    `json:"templateID"`
	BuildID    string    `json:"buildID"`
	CreatedAt  time.Time `json:"createdAt"`
}

// --- Registry ---

// Registry manages the lifecycle and selection of backend adapters.
type Registry struct {
	adapters map[string]SandboxAdapter
	mu       sync.RWMutex
}

// NewRegistry creates a new adapter registry.
func NewRegistry() *Registry {
	return &Registry{
		adapters: make(map[string]SandboxAdapter),
	}
}

// Register adds an adapter to the registry.
func (r *Registry) Register(a SandboxAdapter) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := a.Name()
	if _, exists := r.adapters[name]; exists {
		return fmt.Errorf("adapter %q already registered", name)
	}
	r.adapters[name] = a
	return nil
}

// Get retrieves an adapter by name.
func (r *Registry) Get(name string) (SandboxAdapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[name]
	return a, ok
}

// List returns all registered adapters.
func (r *Registry) List() []SandboxAdapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]SandboxAdapter, 0, len(r.adapters))
	for _, a := range r.adapters {
		result = append(result, a)
	}
	return result
}

// HealthCheckAll checks health of all registered adapters.
func (r *Registry) HealthCheckAll(ctx context.Context) map[string]error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	results := make(map[string]error, len(r.adapters))
	for name, a := range r.adapters {
		results[name] = a.HealthCheck(ctx)
	}
	return results
}

// New creates a new adapter from configuration.
func New(cfg config.BackendConfig) (SandboxAdapter, error) {
	switch cfg.Type {
	case "e2b-cloud":
		return nil, fmt.Errorf("e2b-cloud adapter must be registered via server")
	case "agent-sandbox":
		return nil, fmt.Errorf("agent-sandbox adapter not yet implemented")
	case "opensandbox":
		return nil, fmt.Errorf("opensandbox adapter not yet implemented")
	default:
		return nil, fmt.Errorf("unknown adapter type: %s", cfg.Type)
	}
}
