// Package mock provides a comprehensive mock backend adapter for testing.
// MockBackend wraps an internal stateful mock with call tracking and
// configurable per-method behavior, so tests can observe and override
// every interaction with the adapter.SandboxAdapter interface.
package mock

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	internalmock "github.com/e2bgateway/e2bgateway/internal/adapter/mock"
)

// ---------------------------------------------------------------------------
// Call tracking
// ---------------------------------------------------------------------------

// MethodCall records a single method invocation on MockBackend.
type MethodCall struct {
	Method string
	Args   []interface{}
	Time   time.Time
}

// ---------------------------------------------------------------------------
// MockBackend
// ---------------------------------------------------------------------------

// MockBackend implements adapter.SandboxAdapter for testing.
//
// Every interface method has an optional function field. When the field is
// non-nil the method delegates to it; otherwise the call is forwarded to an
// internal mock.Adapter that provides realistic default responses backed by
// in-memory state.
//
// All calls are recorded in the Calls slice (protected by mu) so tests can
// assert on invocation counts, argument values, and ordering.
type MockBackend struct {
	mu     sync.Mutex
	Calls  []MethodCall
	mock   *internalmock.Adapter // internal stateful mock for defaults

	// Identity / health
	NameFn        func() string
	HealthCheckFn func(ctx context.Context) error

	// Sandbox lifecycle
	CreateSandboxFn func(ctx context.Context, req *adapter.CreateSandboxRequest) (*adapter.Sandbox, error)
	ListSandboxesFn func(ctx context.Context, opts adapter.ListOptions) ([]*adapter.Sandbox, error)
	GetSandboxFn    func(ctx context.Context, id string) (*adapter.Sandbox, error)
	KillSandboxFn   func(ctx context.Context, id string) error
	PauseSandboxFn  func(ctx context.Context, id string) error
	ResumeSandboxFn func(ctx context.Context, id string) (*adapter.Sandbox, error)
	SetTimeoutFn    func(ctx context.Context, id string, timeout time.Duration) error

	// Code execution
	ExecuteCodeFn       func(ctx context.Context, id string, req *adapter.CodeExecutionRequest) (*adapter.CodeExecutionResult, error)
	ExecuteCodeStreamFn func(ctx context.Context, id string, req *adapter.CodeExecutionRequest, stream adapter.CodeStream) error
	RunCommandFn        func(ctx context.Context, id string, req *adapter.CommandRequest) (*adapter.CommandResult, error)

	// Filesystem
	WriteFileFn   func(ctx context.Context, id string, req *adapter.FileWriteRequest) error
	ReadFileFn    func(ctx context.Context, id string, path string) (*adapter.FileContent, error)
	UploadFileFn  func(ctx context.Context, id string, req *adapter.FileUploadRequest) error
	DownloadFileFn func(ctx context.Context, id string, path string) (io.ReadCloser, error)
	ListFilesFn   func(ctx context.Context, id string, path string) ([]adapter.FileInfo, error)
	MakeDirFn     func(ctx context.Context, id string, path string) error
	RemoveFileFn  func(ctx context.Context, id string, path string) error
	MoveFileFn    func(ctx context.Context, id string, src string, dst string) error

	// Templates
	ListTemplatesFn  func(ctx context.Context, opts adapter.ListOptions) ([]*adapter.Template, error)
	GetTemplateFn    func(ctx context.Context, id string) (*adapter.Template, error)
	CreateTemplateFn func(ctx context.Context, req *adapter.CreateTemplateRequest) (*adapter.TemplateBuild, error)
	DeleteTemplateFn func(ctx context.Context, id string) error

	// Template builds
	TriggerBuildFn   func(ctx context.Context, id string, req *adapter.BuildRequest) (*adapter.TemplateBuild, error)
	GetBuildStatusFn func(ctx context.Context, templateID string, buildID string) (*adapter.BuildStatus, error)

	// Template aliases
	CreateAliasFn func(ctx context.Context, templateID string, alias string) error
	DeleteAliasFn func(ctx context.Context, templateID string, alias string) error

	// Warm pools
	ListWarmPoolsFn    func(ctx context.Context) ([]*adapter.WarmPool, error)
	CreateWarmPoolFn   func(ctx context.Context, req *adapter.WarmPoolCreateRequest) (*adapter.WarmPool, error)
	GetWarmPoolFn      func(ctx context.Context, id string) (*adapter.WarmPool, error)
	DeleteWarmPoolFn   func(ctx context.Context, id string) error
	UpdateWarmPoolSizeFn func(ctx context.Context, id string, size int) error

	// Processes
	ListProcessesFn func(ctx context.Context, id string) ([]*adapter.ProcessInfo, error)
	KillProcessFn   func(ctx context.Context, sandboxID string, processID string) error
	SendStdinFn     func(ctx context.Context, sandboxID string, processID string, data string) error

	// Snapshots
	CreateSnapshotFn func(ctx context.Context, id string, req *adapter.SnapshotRequest) (*adapter.Snapshot, error)
	ListSnapshotsFn  func(ctx context.Context, id string) ([]*adapter.Snapshot, error)

	// Ports
	ListPortsFn  func(ctx context.Context, id string) ([]*adapter.PortInfo, error)
	GetPortURLFn func(ctx context.Context, id string, port int) (string, error)

	// Access tokens
	GetAccessTokenFn func(ctx context.Context, id string) (*adapter.AccessToken, error)

	// Environment variables
	SetEnvsFn func(ctx context.Context, id string, envs map[string]string) error

	// Logs
	GetLogsFn func(ctx context.Context, id string) ([]*adapter.LogEntry, error)

	// Template tags
	CreateTagFn func(ctx context.Context, templateID string, req *adapter.TagRequest) (*adapter.Tag, error)
	ListTagsFn  func(ctx context.Context, templateID string) ([]*adapter.Tag, error)
	DeleteTagFn func(ctx context.Context, templateID string, tagName string) error
}

// NewMockBackend creates a MockBackend backed by a default mock.Adapter for
// realistic in-memory state.
func NewMockBackend() *MockBackend {
	return &MockBackend{
		mock: internalmock.New(),
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// ResetCalls clears the recorded call log.
func (m *MockBackend) ResetCalls() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = nil
}

// CallCount returns how many times the named method was invoked.
func (m *MockBackend) CallCount(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.Calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

// CallsSnapshot returns a copy of the recorded calls, safe for concurrent use.
func (m *MockBackend) CallsSnapshot() []MethodCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]MethodCall, len(m.Calls))
	copy(out, m.Calls)
	return out
}

// record appends a MethodCall. Caller must NOT hold m.mu.
func (m *MockBackend) record(method string, args ...interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, MethodCall{
		Method: method,
		Args:   args,
		Time:   time.Now(),
	})
}

// Compile-time interface check.
var _ adapter.SandboxAdapter = (*MockBackend)(nil)

// ---------------------------------------------------------------------------
// Identity / health
// ---------------------------------------------------------------------------

func (m *MockBackend) Name() string {
	if m.NameFn != nil {
		return m.NameFn()
	}
	return "mock-backend"
}

func (m *MockBackend) HealthCheck(ctx context.Context) error {
	m.record("HealthCheck", ctx)
	if m.HealthCheckFn != nil {
		return m.HealthCheckFn(ctx)
	}
	return m.mock.HealthCheck(ctx)
}

// ---------------------------------------------------------------------------
// Sandbox lifecycle
// ---------------------------------------------------------------------------

func (m *MockBackend) CreateSandbox(ctx context.Context, req *adapter.CreateSandboxRequest) (*adapter.Sandbox, error) {
	m.record("CreateSandbox", ctx, req)
	if m.CreateSandboxFn != nil {
		return m.CreateSandboxFn(ctx, req)
	}
	return m.mock.CreateSandbox(ctx, req)
}

func (m *MockBackend) ListSandboxes(ctx context.Context, opts adapter.ListOptions) ([]*adapter.Sandbox, error) {
	m.record("ListSandboxes", ctx, opts)
	if m.ListSandboxesFn != nil {
		return m.ListSandboxesFn(ctx, opts)
	}
	return m.mock.ListSandboxes(ctx, opts)
}

func (m *MockBackend) GetSandbox(ctx context.Context, id string) (*adapter.Sandbox, error) {
	m.record("GetSandbox", ctx, id)
	if m.GetSandboxFn != nil {
		return m.GetSandboxFn(ctx, id)
	}
	return m.mock.GetSandbox(ctx, id)
}

func (m *MockBackend) KillSandbox(ctx context.Context, id string) error {
	m.record("KillSandbox", ctx, id)
	if m.KillSandboxFn != nil {
		return m.KillSandboxFn(ctx, id)
	}
	return m.mock.KillSandbox(ctx, id)
}

func (m *MockBackend) PauseSandbox(ctx context.Context, id string) error {
	m.record("PauseSandbox", ctx, id)
	if m.PauseSandboxFn != nil {
		return m.PauseSandboxFn(ctx, id)
	}
	return m.mock.PauseSandbox(ctx, id)
}

func (m *MockBackend) ResumeSandbox(ctx context.Context, id string) (*adapter.Sandbox, error) {
	m.record("ResumeSandbox", ctx, id)
	if m.ResumeSandboxFn != nil {
		return m.ResumeSandboxFn(ctx, id)
	}
	return m.mock.ResumeSandbox(ctx, id)
}

func (m *MockBackend) SetTimeout(ctx context.Context, id string, timeout time.Duration) error {
	m.record("SetTimeout", ctx, id, timeout)
	if m.SetTimeoutFn != nil {
		return m.SetTimeoutFn(ctx, id, timeout)
	}
	return m.mock.SetTimeout(ctx, id, timeout)
}

// ---------------------------------------------------------------------------
// Code execution
// ---------------------------------------------------------------------------

func (m *MockBackend) ExecuteCode(ctx context.Context, id string, req *adapter.CodeExecutionRequest) (*adapter.CodeExecutionResult, error) {
	m.record("ExecuteCode", ctx, id, req)
	if m.ExecuteCodeFn != nil {
		return m.ExecuteCodeFn(ctx, id, req)
	}
	return m.mock.ExecuteCode(ctx, id, req)
}

func (m *MockBackend) ExecuteCodeStream(ctx context.Context, id string, req *adapter.CodeExecutionRequest, stream adapter.CodeStream) error {
	m.record("ExecuteCodeStream", ctx, id, req, stream)
	if m.ExecuteCodeStreamFn != nil {
		return m.ExecuteCodeStreamFn(ctx, id, req, stream)
	}
	return m.mock.ExecuteCodeStream(ctx, id, req, stream)
}

func (m *MockBackend) RunCommand(ctx context.Context, id string, req *adapter.CommandRequest) (*adapter.CommandResult, error) {
	m.record("RunCommand", ctx, id, req)
	if m.RunCommandFn != nil {
		return m.RunCommandFn(ctx, id, req)
	}
	return m.mock.RunCommand(ctx, id, req)
}

// ---------------------------------------------------------------------------
// Filesystem
// ---------------------------------------------------------------------------

func (m *MockBackend) WriteFile(ctx context.Context, id string, req *adapter.FileWriteRequest) error {
	m.record("WriteFile", ctx, id, req)
	if m.WriteFileFn != nil {
		return m.WriteFileFn(ctx, id, req)
	}
	return m.mock.WriteFile(ctx, id, req)
}

func (m *MockBackend) ReadFile(ctx context.Context, id string, path string) (*adapter.FileContent, error) {
	m.record("ReadFile", ctx, id, path)
	if m.ReadFileFn != nil {
		return m.ReadFileFn(ctx, id, path)
	}
	return m.mock.ReadFile(ctx, id, path)
}

func (m *MockBackend) UploadFile(ctx context.Context, id string, req *adapter.FileUploadRequest) error {
	m.record("UploadFile", ctx, id, req)
	if m.UploadFileFn != nil {
		return m.UploadFileFn(ctx, id, req)
	}
	return m.mock.UploadFile(ctx, id, req)
}

func (m *MockBackend) DownloadFile(ctx context.Context, id string, path string) (io.ReadCloser, error) {
	m.record("DownloadFile", ctx, id, path)
	if m.DownloadFileFn != nil {
		return m.DownloadFileFn(ctx, id, path)
	}
	// The internal mock returns an error for DownloadFile; intercept success
	// path by reading from the mock's ReadFile instead so tests get usable data.
	fc, err := m.mock.ReadFile(ctx, id, path)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader(string(fc.Content))), nil
}

func (m *MockBackend) ListFiles(ctx context.Context, id string, path string) ([]adapter.FileInfo, error) {
	m.record("ListFiles", ctx, id, path)
	if m.ListFilesFn != nil {
		return m.ListFilesFn(ctx, id, path)
	}
	return m.mock.ListFiles(ctx, id, path)
}

func (m *MockBackend) MakeDir(ctx context.Context, id string, path string) error {
	m.record("MakeDir", ctx, id, path)
	if m.MakeDirFn != nil {
		return m.MakeDirFn(ctx, id, path)
	}
	return m.mock.MakeDir(ctx, id, path)
}

func (m *MockBackend) RemoveFile(ctx context.Context, id string, path string) error {
	m.record("RemoveFile", ctx, id, path)
	if m.RemoveFileFn != nil {
		return m.RemoveFileFn(ctx, id, path)
	}
	return m.mock.RemoveFile(ctx, id, path)
}

func (m *MockBackend) MoveFile(ctx context.Context, id string, src string, dst string) error {
	m.record("MoveFile", ctx, id, src, dst)
	if m.MoveFileFn != nil {
		return m.MoveFileFn(ctx, id, src, dst)
	}
	return m.mock.MoveFile(ctx, id, src, dst)
}

// ---------------------------------------------------------------------------
// Templates
// ---------------------------------------------------------------------------

func (m *MockBackend) ListTemplates(ctx context.Context, opts adapter.ListOptions) ([]*adapter.Template, error) {
	m.record("ListTemplates", ctx, opts)
	if m.ListTemplatesFn != nil {
		return m.ListTemplatesFn(ctx, opts)
	}
	return m.mock.ListTemplates(ctx, opts)
}

func (m *MockBackend) GetTemplate(ctx context.Context, id string) (*adapter.Template, error) {
	m.record("GetTemplate", ctx, id)
	if m.GetTemplateFn != nil {
		return m.GetTemplateFn(ctx, id)
	}
	return m.mock.GetTemplate(ctx, id)
}

func (m *MockBackend) CreateTemplate(ctx context.Context, req *adapter.CreateTemplateRequest) (*adapter.TemplateBuild, error) {
	m.record("CreateTemplate", ctx, req)
	if m.CreateTemplateFn != nil {
		return m.CreateTemplateFn(ctx, req)
	}
	return m.mock.CreateTemplate(ctx, req)
}

func (m *MockBackend) DeleteTemplate(ctx context.Context, id string) error {
	m.record("DeleteTemplate", ctx, id)
	if m.DeleteTemplateFn != nil {
		return m.DeleteTemplateFn(ctx, id)
	}
	return m.mock.DeleteTemplate(ctx, id)
}

// ---------------------------------------------------------------------------
// Template builds
// ---------------------------------------------------------------------------

func (m *MockBackend) TriggerBuild(ctx context.Context, id string, req *adapter.BuildRequest) (*adapter.TemplateBuild, error) {
	m.record("TriggerBuild", ctx, id, req)
	if m.TriggerBuildFn != nil {
		return m.TriggerBuildFn(ctx, id, req)
	}
	return m.mock.TriggerBuild(ctx, id, req)
}

func (m *MockBackend) GetBuildStatus(ctx context.Context, templateID string, buildID string) (*adapter.BuildStatus, error) {
	m.record("GetBuildStatus", ctx, templateID, buildID)
	if m.GetBuildStatusFn != nil {
		return m.GetBuildStatusFn(ctx, templateID, buildID)
	}
	return m.mock.GetBuildStatus(ctx, templateID, buildID)
}

// ---------------------------------------------------------------------------
// Template aliases
// ---------------------------------------------------------------------------

func (m *MockBackend) CreateAlias(ctx context.Context, templateID string, alias string) error {
	m.record("CreateAlias", ctx, templateID, alias)
	if m.CreateAliasFn != nil {
		return m.CreateAliasFn(ctx, templateID, alias)
	}
	return m.mock.CreateAlias(ctx, templateID, alias)
}

func (m *MockBackend) DeleteAlias(ctx context.Context, templateID string, alias string) error {
	m.record("DeleteAlias", ctx, templateID, alias)
	if m.DeleteAliasFn != nil {
		return m.DeleteAliasFn(ctx, templateID, alias)
	}
	return m.mock.DeleteAlias(ctx, templateID, alias)
}

// ---------------------------------------------------------------------------
// Warm pools
// ---------------------------------------------------------------------------

func (m *MockBackend) ListWarmPools(ctx context.Context) ([]*adapter.WarmPool, error) {
	m.record("ListWarmPools", ctx)
	if m.ListWarmPoolsFn != nil {
		return m.ListWarmPoolsFn(ctx)
	}
	return m.mock.ListWarmPools(ctx)
}

func (m *MockBackend) CreateWarmPool(ctx context.Context, req *adapter.WarmPoolCreateRequest) (*adapter.WarmPool, error) {
	m.record("CreateWarmPool", ctx, req)
	if m.CreateWarmPoolFn != nil {
		return m.CreateWarmPoolFn(ctx, req)
	}
	return m.mock.CreateWarmPool(ctx, req)
}

func (m *MockBackend) GetWarmPool(ctx context.Context, id string) (*adapter.WarmPool, error) {
	m.record("GetWarmPool", ctx, id)
	if m.GetWarmPoolFn != nil {
		return m.GetWarmPoolFn(ctx, id)
	}
	return m.mock.GetWarmPool(ctx, id)
}

func (m *MockBackend) DeleteWarmPool(ctx context.Context, id string) error {
	m.record("DeleteWarmPool", ctx, id)
	if m.DeleteWarmPoolFn != nil {
		return m.DeleteWarmPoolFn(ctx, id)
	}
	return m.mock.DeleteWarmPool(ctx, id)
}

func (m *MockBackend) UpdateWarmPoolSize(ctx context.Context, id string, size int) error {
	m.record("UpdateWarmPoolSize", ctx, id, size)
	if m.UpdateWarmPoolSizeFn != nil {
		return m.UpdateWarmPoolSizeFn(ctx, id, size)
	}
	return m.mock.UpdateWarmPoolSize(ctx, id, size)
}

// ---------------------------------------------------------------------------
// Processes
// ---------------------------------------------------------------------------

func (m *MockBackend) ListProcesses(ctx context.Context, id string) ([]*adapter.ProcessInfo, error) {
	m.record("ListProcesses", ctx, id)
	if m.ListProcessesFn != nil {
		return m.ListProcessesFn(ctx, id)
	}
	return m.mock.ListProcesses(ctx, id)
}

func (m *MockBackend) KillProcess(ctx context.Context, sandboxID string, processID string) error {
	m.record("KillProcess", ctx, sandboxID, processID)
	if m.KillProcessFn != nil {
		return m.KillProcessFn(ctx, sandboxID, processID)
	}
	return m.mock.KillProcess(ctx, sandboxID, processID)
}

func (m *MockBackend) SendStdin(ctx context.Context, sandboxID string, processID string, data string) error {
	m.record("SendStdin", ctx, sandboxID, processID, data)
	if m.SendStdinFn != nil {
		return m.SendStdinFn(ctx, sandboxID, processID, data)
	}
	return m.mock.SendStdin(ctx, sandboxID, processID, data)
}

// ---------------------------------------------------------------------------
// Snapshots
// ---------------------------------------------------------------------------

func (m *MockBackend) CreateSnapshot(ctx context.Context, id string, req *adapter.SnapshotRequest) (*adapter.Snapshot, error) {
	m.record("CreateSnapshot", ctx, id, req)
	if m.CreateSnapshotFn != nil {
		return m.CreateSnapshotFn(ctx, id, req)
	}
	return m.mock.CreateSnapshot(ctx, id, req)
}

func (m *MockBackend) ListSnapshots(ctx context.Context, id string) ([]*adapter.Snapshot, error) {
	m.record("ListSnapshots", ctx, id)
	if m.ListSnapshotsFn != nil {
		return m.ListSnapshotsFn(ctx, id)
	}
	return m.mock.ListSnapshots(ctx, id)
}

// ---------------------------------------------------------------------------
// Ports
// ---------------------------------------------------------------------------

func (m *MockBackend) ListPorts(ctx context.Context, id string) ([]*adapter.PortInfo, error) {
	m.record("ListPorts", ctx, id)
	if m.ListPortsFn != nil {
		return m.ListPortsFn(ctx, id)
	}
	return m.mock.ListPorts(ctx, id)
}

func (m *MockBackend) GetPortURL(ctx context.Context, id string, port int) (string, error) {
	m.record("GetPortURL", ctx, id, port)
	if m.GetPortURLFn != nil {
		return m.GetPortURLFn(ctx, id, port)
	}
	return m.mock.GetPortURL(ctx, id, port)
}

// ---------------------------------------------------------------------------
// Access tokens
// ---------------------------------------------------------------------------

func (m *MockBackend) GetAccessToken(ctx context.Context, id string) (*adapter.AccessToken, error) {
	m.record("GetAccessToken", ctx, id)
	if m.GetAccessTokenFn != nil {
		return m.GetAccessTokenFn(ctx, id)
	}
	return m.mock.GetAccessToken(ctx, id)
}

// ---------------------------------------------------------------------------
// Environment variables
// ---------------------------------------------------------------------------

func (m *MockBackend) SetEnvs(ctx context.Context, id string, envs map[string]string) error {
	m.record("SetEnvs", ctx, id, envs)
	if m.SetEnvsFn != nil {
		return m.SetEnvsFn(ctx, id, envs)
	}
	return m.mock.SetEnvs(ctx, id, envs)
}

// ---------------------------------------------------------------------------
// Logs
// ---------------------------------------------------------------------------

func (m *MockBackend) GetLogs(ctx context.Context, id string) ([]*adapter.LogEntry, error) {
	m.record("GetLogs", ctx, id)
	if m.GetLogsFn != nil {
		return m.GetLogsFn(ctx, id)
	}
	return m.mock.GetLogs(ctx, id)
}

// ---------------------------------------------------------------------------
// Template tags
// ---------------------------------------------------------------------------

func (m *MockBackend) CreateTag(ctx context.Context, templateID string, req *adapter.TagRequest) (*adapter.Tag, error) {
	m.record("CreateTag", ctx, templateID, req)
	if m.CreateTagFn != nil {
		return m.CreateTagFn(ctx, templateID, req)
	}
	return m.mock.CreateTag(ctx, templateID, req)
}

func (m *MockBackend) ListTags(ctx context.Context, templateID string) ([]*adapter.Tag, error) {
	m.record("ListTags", ctx, templateID)
	if m.ListTagsFn != nil {
		return m.ListTagsFn(ctx, templateID)
	}
	return m.mock.ListTags(ctx, templateID)
}

func (m *MockBackend) DeleteTag(ctx context.Context, templateID string, tagName string) error {
	m.record("DeleteTag", ctx, templateID, tagName)
	if m.DeleteTagFn != nil {
		return m.DeleteTagFn(ctx, templateID, tagName)
	}
	return m.mock.DeleteTag(ctx, templateID, tagName)
}

