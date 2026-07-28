package routing

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/config"
)

// stubAdapter is a minimal adapter for testing.
type stubAdapter struct {
	name    string
	healthy bool
}

func (s *stubAdapter) Name() string { return s.name }
func (s *stubAdapter) HealthCheck(ctx context.Context) error {
	if !s.healthy {
		return io.EOF
	}
	return nil
}
func (s *stubAdapter) CreateSandbox(ctx context.Context, req *adapter.CreateSandboxRequest) (*adapter.Sandbox, error) {
	return nil, nil
}
func (s *stubAdapter) ListSandboxes(ctx context.Context, opts adapter.ListOptions) ([]*adapter.Sandbox, error) {
	return nil, nil
}
func (s *stubAdapter) GetSandbox(ctx context.Context, id string) (*adapter.Sandbox, error) {
	return nil, nil
}
func (s *stubAdapter) KillSandbox(ctx context.Context, id string) error  { return nil }
func (s *stubAdapter) PauseSandbox(ctx context.Context, id string) error { return nil }
func (s *stubAdapter) ResumeSandbox(ctx context.Context, id string) (*adapter.Sandbox, error) {
	return nil, nil
}
func (s *stubAdapter) SetTimeout(ctx context.Context, id string, timeout time.Duration) error {
	return nil
}
func (s *stubAdapter) ExecuteCode(ctx context.Context, id string, req *adapter.CodeExecutionRequest) (*adapter.CodeExecutionResult, error) {
	return nil, nil
}
func (s *stubAdapter) ExecuteCodeStream(ctx context.Context, id string, req *adapter.CodeExecutionRequest, stream adapter.CodeStream) error {
	return nil
}
func (s *stubAdapter) RunCommand(ctx context.Context, id string, req *adapter.CommandRequest) (*adapter.CommandResult, error) {
	return nil, nil
}
func (s *stubAdapter) WriteFile(ctx context.Context, id string, req *adapter.FileWriteRequest) error {
	return nil
}
func (s *stubAdapter) ReadFile(ctx context.Context, id, path string) (*adapter.FileContent, error) {
	return nil, nil
}
func (s *stubAdapter) UploadFile(ctx context.Context, id string, req *adapter.FileUploadRequest) error {
	return nil
}
func (s *stubAdapter) DownloadFile(ctx context.Context, id, path string) (io.ReadCloser, error) {
	return nil, nil
}
func (s *stubAdapter) ListFiles(ctx context.Context, id, path string) ([]adapter.FileInfo, error) {
	return nil, nil
}
func (s *stubAdapter) MakeDir(ctx context.Context, id, path string) error    { return nil }
func (s *stubAdapter) RemoveFile(ctx context.Context, id, path string) error { return nil }
func (s *stubAdapter) ListTemplates(ctx context.Context, opts adapter.ListOptions) ([]*adapter.Template, error) {
	return nil, nil
}
func (s *stubAdapter) GetTemplate(ctx context.Context, id string) (*adapter.Template, error) {
	return nil, nil
}
func (s *stubAdapter) CreateTemplate(ctx context.Context, req *adapter.CreateTemplateRequest) (*adapter.TemplateBuild, error) {
	return nil, nil
}
func (s *stubAdapter) DeleteTemplate(ctx context.Context, id string) error { return nil }
func (s *stubAdapter) TriggerBuild(ctx context.Context, id string, req *adapter.BuildRequest) (*adapter.TemplateBuild, error) {
	return nil, nil
}
func (s *stubAdapter) GetBuildStatus(ctx context.Context, templateID, buildID string) (*adapter.BuildStatus, error) {
	return nil, nil
}
func (s *stubAdapter) CreateAlias(ctx context.Context, templateID, alias string) error { return nil }
func (s *stubAdapter) DeleteAlias(ctx context.Context, templateID, alias string) error { return nil }
func (s *stubAdapter) ListWarmPools(ctx context.Context) ([]*adapter.WarmPool, error) {
	return nil, nil
}
func (s *stubAdapter) CreateWarmPool(ctx context.Context, req *adapter.WarmPoolCreateRequest) (*adapter.WarmPool, error) {
	return nil, nil
}
func (s *stubAdapter) GetWarmPool(ctx context.Context, id string) (*adapter.WarmPool, error) {
	return nil, nil
}
func (s *stubAdapter) DeleteWarmPool(ctx context.Context, id string) error { return nil }
func (s *stubAdapter) UpdateWarmPoolSize(ctx context.Context, id string, size int) error { return nil }
func (s *stubAdapter) ListProcesses(ctx context.Context, id string) ([]*adapter.ProcessInfo, error) {
	return nil, nil
}
func (s *stubAdapter) KillProcess(ctx context.Context, sandboxID, processID string) error { return nil }
func (s *stubAdapter) SendStdin(ctx context.Context, sandboxID, processID, data string) error {
	return nil
}
func (s *stubAdapter) CreateSnapshot(ctx context.Context, id string, req *adapter.SnapshotRequest) (*adapter.Snapshot, error) {
	return nil, nil
}
func (s *stubAdapter) ListSnapshots(ctx context.Context, id string) ([]*adapter.Snapshot, error) {
	return nil, nil
}
func (s *stubAdapter) ListPorts(ctx context.Context, id string) ([]*adapter.PortInfo, error) {
	return nil, nil
}
func (s *stubAdapter) GetPortURL(ctx context.Context, id string, port int) (string, error) {
	return "", nil
}
func (s *stubAdapter) GetAccessToken(ctx context.Context, id string) (*adapter.AccessToken, error) {
	return nil, nil
}
func (s *stubAdapter) SetEnvs(ctx context.Context, id string, envs map[string]string) error {
	return nil
}
func (s *stubAdapter) GetLogs(ctx context.Context, id string) ([]*adapter.LogEntry, error) {
	return nil, nil
}
func (s *stubAdapter) MoveFile(ctx context.Context, id, src, dst string) error {
	return nil
}
func (s *stubAdapter) CreateTag(ctx context.Context, templateID string, req *adapter.TagRequest) (*adapter.Tag, error) {
	return nil, nil
}
func (s *stubAdapter) ListTags(ctx context.Context, templateID string) ([]*adapter.Tag, error) {
	return nil, nil
}
func (s *stubAdapter) DeleteTag(ctx context.Context, templateID, tagName string) error {
	return nil
}
func (s *stubAdapter) GetEnvdEndpoint(ctx context.Context, id string) (string, string, error) {
	return "", "", nil
}

func newTestRegistry() *adapter.Registry {
	reg := adapter.NewRegistry()
	reg.Register(&stubAdapter{name: "agent-sandbox", healthy: true})
	reg.Register(&stubAdapter{name: "e2b-cloud", healthy: true})
	reg.Register(&stubAdapter{name: "opensandbox", healthy: true})
	return reg
}

func TestRouter_TemplateRouting(t *testing.T) {
	reg := newTestRegistry()
	r := NewRouter(config.RoutingConfig{
		Strategies: []config.RoutingStrategy{
			{
				Name: "template-routing",
				Rules: []config.RoutingRule{
					{Template: "code-interpreter", Backend: "agent-sandbox"},
					{Template: "browser", Backend: "opensandbox"},
					{Template: "desktop", Backend: "e2b-cloud"},
				},
			},
		},
	}, reg)
	defer r.Stop()

	tests := []struct {
		template string
		expected string
	}{
		{"code-interpreter", "agent-sandbox"},
		{"browser", "opensandbox"},
		{"desktop", "e2b-cloud"},
	}

	for _, tt := range tests {
		backend, err := r.SelectBackend(context.Background(), &RoutingRequest{TemplateID: tt.template})
		if err != nil {
			t.Fatalf("SelectBackend(template=%s) error: %v", tt.template, err)
		}
		if backend != tt.expected {
			t.Errorf("template %s: expected %s, got %s", tt.template, tt.expected, backend)
		}
	}
}

func TestRouter_TenantRouting(t *testing.T) {
	reg := newTestRegistry()
	r := NewRouter(config.RoutingConfig{
		Strategies: []config.RoutingStrategy{
			{
				Name: "tenant-static",
				Rules: []config.RoutingRule{
					{Tenant: "acme-corp", Backend: "agent-sandbox"},
					{Tenant: "beta-users", Backend: "e2b-cloud"},
				},
			},
		},
	}, reg)
	defer r.Stop()

	backend, err := r.SelectBackend(context.Background(), &RoutingRequest{TenantID: "acme-corp"})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if backend != "agent-sandbox" {
		t.Errorf("expected agent-sandbox, got %s", backend)
	}

	backend, err = r.SelectBackend(context.Background(), &RoutingRequest{TenantID: "beta-users"})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if backend != "e2b-cloud" {
		t.Errorf("expected e2b-cloud, got %s", backend)
	}
}

func TestRouter_DefaultBackend(t *testing.T) {
	reg := newTestRegistry()
	r := NewRouter(config.RoutingConfig{
		DefaultBackend: "e2b-cloud",
	}, reg)
	defer r.Stop()

	backend, err := r.SelectBackend(context.Background(), &RoutingRequest{})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if backend != "e2b-cloud" {
		t.Errorf("expected e2b-cloud, got %s", backend)
	}
}

func TestRouter_FallbackToFirstAvailable(t *testing.T) {
	reg := adapter.NewRegistry()
	reg.Register(&stubAdapter{name: "only-one", healthy: true})

	r := NewRouter(config.RoutingConfig{}, reg)
	defer r.Stop()

	backend, err := r.SelectBackend(context.Background(), &RoutingRequest{})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if backend != "only-one" {
		t.Errorf("expected only-one, got %s", backend)
	}
}

func TestRouter_Failover(t *testing.T) {
	reg := newTestRegistry()
	r := NewRouter(config.RoutingConfig{
		DefaultBackend: "nonexistent",
		Failover: config.FailoverConfig{
			Enabled: true,
			Chain:   []string{"nonexistent", "agent-sandbox", "e2b-cloud"},
		},
	}, reg)
	defer r.Stop()

	backend, err := r.SelectBackend(context.Background(), &RoutingRequest{})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	// Should skip nonexistent and find agent-sandbox
	if backend != "agent-sandbox" {
		t.Errorf("expected agent-sandbox, got %s", backend)
	}
}

func TestRouter_NoBackendAvailable(t *testing.T) {
	reg := adapter.NewRegistry()
	r := NewRouter(config.RoutingConfig{}, reg)
	defer r.Stop()

	_, err := r.SelectBackend(context.Background(), &RoutingRequest{})
	if err == nil {
		t.Error("expected error when no backends available")
	}
}

func TestRouter_HealthStatus(t *testing.T) {
	reg := newTestRegistry()
	r := NewRouter(config.RoutingConfig{}, reg)
	defer r.Stop()

	status := r.GetHealthStatus()
	if len(status) != 3 {
		t.Errorf("expected 3 backends in health status, got %d", len(status))
	}

	for name, healthy := range status {
		if !healthy {
			t.Errorf("expected backend %s to be healthy", name)
		}
	}
}

func TestRouter_IsHealthy(t *testing.T) {
	reg := newTestRegistry()
	r := NewRouter(config.RoutingConfig{}, reg)
	defer r.Stop()

	// Mark a backend as unhealthy
	r.healthMu.Lock()
	r.healthStatus["e2b-cloud"].healthy = false
	r.healthMu.Unlock()

	if !r.isHealthy("agent-sandbox") {
		t.Error("expected agent-sandbox to be healthy")
	}
	if r.isHealthy("e2b-cloud") {
		t.Error("expected e2b-cloud to be unhealthy")
	}
	// Unknown backend should be assumed healthy
	if !r.isHealthy("unknown") {
		t.Error("expected unknown backend to be assumed healthy")
	}
}
