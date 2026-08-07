package e2bcloud

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/api/dto"
	"github.com/e2bgateway/e2bgateway/internal/config"
)

// Adapter implements the SandboxAdapter interface for E2B Cloud.
type Adapter struct {
	client *Client
	name   string
}

// NewAdapter creates a new E2B Cloud adapter from configuration.
func NewAdapter(cfg config.BackendConfig) (*Adapter, error) {
	endpoint, _ := cfg.Config["endpoint"].(string)
	apiKey, _ := cfg.Config["apiKey"].(string)

	if endpoint == "" {
		endpoint = "https://api.e2b.dev"
	}

	client := NewClient(ClientConfig{
		Endpoint: endpoint,
		APIKey:   apiKey,
	})

	return &Adapter{
		client: client,
		name:   cfg.Name,
	}, nil
}

// NewAdapterWithClient creates an adapter with a pre-configured client (for testing).
func NewAdapterWithClient(name string, client *Client) *Adapter {
	return &Adapter{client: client, name: name}
}

func (a *Adapter) Name() string { return a.name }

func (a *Adapter) HealthCheck(ctx context.Context) error {
	_, err := a.client.ListSandboxes(ctx)
	return err
}

// --- Sandbox Lifecycle ---

func (a *Adapter) CreateSandbox(ctx context.Context, req *adapter.CreateSandboxRequest) (*adapter.Sandbox, error) {
	dtoReq := &dto.SandboxCreateRequest{
		TemplateID: req.TemplateID,
		Alias:      req.Alias,
		Timeout:    req.Timeout,
		Metadata:   req.Metadata,
	}

	resp, err := a.client.CreateSandbox(ctx, dtoReq)
	if err != nil {
		return nil, err
	}

	// Fetch full sandbox info
	info, err := a.client.GetSandbox(ctx, resp.SandboxID)
	if err != nil {
		// Return partial info from create response
		return &adapter.Sandbox{
			SandboxID:  resp.SandboxID,
			TemplateID: resp.TemplateID,
			Alias:      resp.Alias,
			Status:     adapter.SandboxStatusStarting,
			Backend:    a.name,
		}, nil
	}

	return dtoToSandbox(info, a.name), nil
}

func (a *Adapter) ListSandboxes(ctx context.Context, opts adapter.ListOptions) ([]*adapter.Sandbox, error) {
	infos, err := a.client.ListSandboxes(ctx)
	if err != nil {
		return nil, err
	}

	// Convert to adapter.Sandbox
	allSandboxes := make([]*adapter.Sandbox, len(infos))
	for i, info := range infos {
		allSandboxes[i] = dtoToSandbox(&info, a.name)
	}

	// Apply client-side pagination
	start := opts.Offset
	if start < 0 {
		start = 0
	}
	if start >= len(allSandboxes) {
		return []*adapter.Sandbox{}, nil
	}

	end := len(allSandboxes)
	if opts.Limit > 0 && start+opts.Limit < end {
		end = start + opts.Limit
	}

	return allSandboxes[start:end], nil
}

func (a *Adapter) GetSandbox(ctx context.Context, sandboxID string) (*adapter.Sandbox, error) {
	info, err := a.client.GetSandbox(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	return dtoToSandbox(info, a.name), nil
}

func (a *Adapter) KillSandbox(ctx context.Context, sandboxID string) error {
	return a.client.KillSandbox(ctx, sandboxID)
}

func (a *Adapter) PauseSandbox(ctx context.Context, sandboxID string) error {
	return a.client.PauseSandbox(ctx, sandboxID)
}

func (a *Adapter) ResumeSandbox(ctx context.Context, sandboxID string) (*adapter.Sandbox, error) {
	resp, err := a.client.ResumeSandbox(ctx, sandboxID, &dto.SandboxResumeRequest{})
	if err != nil {
		return nil, err
	}

	info, err := a.client.GetSandbox(ctx, resp.SandboxID)
	if err != nil {
		return &adapter.Sandbox{
			SandboxID:  resp.SandboxID,
			TemplateID: resp.TemplateID,
			Status:     adapter.SandboxStatusStarting,
			Backend:    a.name,
		}, nil
	}
	return dtoToSandbox(info, a.name), nil
}

func (a *Adapter) SetTimeout(ctx context.Context, sandboxID string, timeout time.Duration) error {
	return a.client.SetTimeout(ctx, sandboxID, &dto.SandboxTimeoutRequest{
		Timeout: int(timeout.Seconds()),
	})
}

// --- Code Execution ---

func (a *Adapter) ExecuteCode(ctx context.Context, sandboxID string, req *adapter.CodeExecutionRequest) (*adapter.CodeExecutionResult, error) {
	dtoReq := &dto.CodeExecRequest{
		Code:     req.Code,
		Language: req.Language,
		EnvVars:  req.EnvVars,
	}

	resp, err := a.client.ExecuteCode(ctx, sandboxID, dtoReq)
	if err != nil {
		return nil, err
	}

	return &adapter.CodeExecutionResult{
		Stdout:   resp.Stdout,
		Stderr:   resp.Stderr,
		ExitCode: resp.ExitCode,
		Error:    resp.Error,
	}, nil
}

func (a *Adapter) ExecuteCodeStream(ctx context.Context, sandboxID string, req *adapter.CodeExecutionRequest, stream adapter.CodeStream) error {
	return fmt.Errorf("streaming not yet implemented for e2b-cloud adapter")
}

func (a *Adapter) RunCommand(ctx context.Context, sandboxID string, req *adapter.CommandRequest) (*adapter.CommandResult, error) {
	dtoReq := &dto.CommandRequest{
		Command: req.Command,
		Cwd:     req.Cwd,
		EnvVars: req.EnvVars,
	}

	resp, err := a.client.RunCommand(ctx, sandboxID, dtoReq)
	if err != nil {
		return nil, err
	}

	return &adapter.CommandResult{
		Stdout:   resp.Stdout,
		Stderr:   resp.Stderr,
		ExitCode: resp.ExitCode,
	}, nil
}

// --- Filesystem ---

func (a *Adapter) WriteFile(ctx context.Context, sandboxID string, req *adapter.FileWriteRequest) error {
	return a.client.WriteFile(ctx, sandboxID, &dto.FileWriteRequest{
		Path:    req.Path,
		Content: string(req.Content),
	})
}

func (a *Adapter) ReadFile(ctx context.Context, sandboxID string, path string) (*adapter.FileContent, error) {
	data, err := a.client.ReadFile(ctx, sandboxID, path)
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
	return a.client.UploadFile(ctx, sandboxID, req.Path, req.Reader)
}

func (a *Adapter) DownloadFile(ctx context.Context, sandboxID string, path string) (io.ReadCloser, error) {
	data, err := a.client.DownloadFile(ctx, sandboxID, path)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (a *Adapter) ListFiles(ctx context.Context, sandboxID string, path string) ([]adapter.FileInfo, error) {
	resp, err := a.client.ListFiles(ctx, sandboxID, &dto.FileListRequest{Path: path})
	if err != nil {
		return nil, err
	}

	result := make([]adapter.FileInfo, len(resp.Entries))
	for i, e := range resp.Entries {
		result[i] = adapter.FileInfo{
			Name:    e.Name,
			Path:    path + "/" + e.Name,
			Size:    e.Size,
			IsDir:   e.Type == "dir",
			ModTime: e.LastModified,
		}
	}
	return result, nil
}

func (a *Adapter) MakeDir(ctx context.Context, sandboxID string, path string) error {
	return a.client.MakeDir(ctx, sandboxID, &dto.MakeDirRequest{Path: path})
}

func (a *Adapter) RemoveFile(ctx context.Context, sandboxID string, path string) error {
	return a.client.RemoveFile(ctx, sandboxID, &dto.FileRemoveRequest{Path: path})
}

// --- Templates ---

func (a *Adapter) ListTemplates(ctx context.Context, opts adapter.ListOptions) ([]*adapter.Template, error) {
	infos, err := a.client.ListTemplates(ctx)
	if err != nil {
		return nil, err
	}

	// Convert to adapter.Template
	allTemplates := make([]*adapter.Template, len(infos))
	for i, info := range infos {
		allTemplates[i] = dtoToTemplate(&info)
	}

	// Apply client-side pagination
	start := opts.Offset
	if start < 0 {
		start = 0
	}
	if start >= len(allTemplates) {
		return []*adapter.Template{}, nil
	}

	end := len(allTemplates)
	if opts.Limit > 0 && start+opts.Limit < end {
		end = start + opts.Limit
	}

	return allTemplates[start:end], nil
}

func (a *Adapter) GetTemplate(ctx context.Context, templateID string) (*adapter.Template, error) {
	info, err := a.client.GetTemplate(ctx, templateID)
	if err != nil {
		return nil, err
	}
	return dtoToTemplate(info), nil
}

func (a *Adapter) CreateTemplate(ctx context.Context, req *adapter.CreateTemplateRequest) (*adapter.TemplateBuild, error) {
	dtoReq := &dto.TemplateBuildRequest{
		Name:       req.Name,
		Dockerfile: req.Dockerfile,
		StartCmd:   req.StartCmd,
		CPUCount:   req.CPUCount,
		MemoryMB:   req.MemoryMB,
	}
	resp, err := a.client.CreateTemplate(ctx, dtoReq)
	if err != nil {
		return nil, err
	}
	return &adapter.TemplateBuild{
		TemplateID: resp.TemplateID,
		BuildID:    resp.BuildID,
		Status:     resp.Status,
	}, nil
}

func (a *Adapter) DeleteTemplate(ctx context.Context, templateID string) error {
	return a.client.DeleteTemplate(ctx, templateID)
}

// --- Template Builds ---

func (a *Adapter) TriggerBuild(ctx context.Context, templateID string, req *adapter.BuildRequest) (*adapter.TemplateBuild, error) {
	dtoReq := &dto.TemplateBuildRequest{
		Dockerfile: req.Dockerfile,
		StartCmd:   req.StartCmd,
	}
	resp, err := a.client.CreateTemplate(ctx, dtoReq)
	if err != nil {
		return nil, err
	}
	return &adapter.TemplateBuild{
		TemplateID: templateID,
		BuildID:    resp.BuildID,
		Status:     resp.Status,
	}, nil
}

func (a *Adapter) GetBuildStatus(ctx context.Context, templateID, buildID string) (*adapter.BuildStatus, error) {
	resp, err := a.client.GetBuildStatus(ctx, templateID, buildID)
	if err != nil {
		return nil, err
	}
	return &adapter.BuildStatus{
		BuildID: resp.BuildID,
		Status:  resp.Status,
		Logs:    resp.Logs,
		Error:   resp.Error,
	}, nil
}

// --- Template Aliases ---

func (a *Adapter) CreateAlias(ctx context.Context, templateID string, alias string) error {
	return a.client.CreateAlias(ctx, templateID, &dto.AliasRequest{Alias: alias})
}

func (a *Adapter) DeleteAlias(ctx context.Context, templateID, alias string) error {
	return a.client.DeleteAlias(ctx, templateID, alias)
}

// --- Warm Pools ---

func (a *Adapter) ListWarmPools(ctx context.Context) ([]*adapter.WarmPool, error) {
	infos, err := a.client.ListWarmPools(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]*adapter.WarmPool, len(infos))
	for i, info := range infos {
		result[i] = &adapter.WarmPool{
			WarmPoolID:  info.WarmPoolID,
			TemplateID:  info.TemplateID,
			TargetSize:  info.TargetSize,
			CurrentSize: info.CurrentSize,
			Status:      info.Status,
			CreatedAt:   info.CreatedAt,
		}
	}
	return result, nil
}

func (a *Adapter) CreateWarmPool(ctx context.Context, req *adapter.WarmPoolCreateRequest) (*adapter.WarmPool, error) {
	dtoReq := &dto.WarmPoolCreateRequest{
		TemplateID: req.TemplateID,
		TargetSize: req.TargetSize,
		WarmPoolID: req.WarmPoolID,
	}
	resp, err := a.client.CreateWarmPool(ctx, dtoReq)
	if err != nil {
		return nil, err
	}
	return &adapter.WarmPool{
		WarmPoolID:  resp.WarmPoolID,
		TemplateID:  resp.TemplateID,
		TargetSize:  resp.TargetSize,
		CurrentSize: resp.CurrentSize,
		Status:      resp.Status,
		CreatedAt:   resp.CreatedAt,
	}, nil
}

func (a *Adapter) GetWarmPool(ctx context.Context, warmPoolID string) (*adapter.WarmPool, error) {
	pools, err := a.client.ListWarmPools(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range pools {
		if p.WarmPoolID == warmPoolID {
			return &adapter.WarmPool{
				WarmPoolID:  p.WarmPoolID,
				TemplateID:  p.TemplateID,
				TargetSize:  p.TargetSize,
				CurrentSize: p.CurrentSize,
				Status:      p.Status,
				CreatedAt:   p.CreatedAt,
			}, nil
		}
	}
	return nil, fmt.Errorf("warm pool %q not found", warmPoolID)
}

func (a *Adapter) DeleteWarmPool(ctx context.Context, warmPoolID string) error {
	return a.client.DeleteWarmPool(ctx, warmPoolID)
}

func (a *Adapter) UpdateWarmPoolSize(ctx context.Context, warmPoolID string, targetSize int) error {
	return a.client.UpdateWarmPoolSize(ctx, warmPoolID, &dto.WarmPoolSizeRequest{
		TargetSize: targetSize,
	})
}

// --- Processes ---

func (a *Adapter) ListProcesses(ctx context.Context, sandboxID string) ([]*adapter.ProcessInfo, error) {
	infos, err := a.client.ListProcesses(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	result := make([]*adapter.ProcessInfo, len(infos))
	for i, info := range infos {
		result[i] = &adapter.ProcessInfo{
			ProcessID: info.ProcessID,
			Command:   info.Command,
			PID:       info.PID,
			Status:    info.Status,
			StartedAt: info.StartedAt,
		}
	}
	return result, nil
}

func (a *Adapter) KillProcess(ctx context.Context, sandboxID, processID string) error {
	return a.client.KillProcess(ctx, sandboxID, processID)
}

func (a *Adapter) SendStdin(ctx context.Context, sandboxID, processID string, data string) error {
	return a.client.SendStdin(ctx, sandboxID, processID, &dto.SendStdinRequest{Data: data})
}

// --- Snapshots ---

func (a *Adapter) CreateSnapshot(ctx context.Context, sandboxID string, req *adapter.SnapshotRequest) (*adapter.Snapshot, error) {
	dtoReq := &dto.SnapshotCreateRequest{
		Name:        req.Name,
		Description: req.Description,
	}
	resp, err := a.client.CreateSnapshot(ctx, sandboxID, dtoReq)
	if err != nil {
		return nil, err
	}
	return &adapter.Snapshot{
		SnapshotID:  resp.SnapshotID,
		SandboxID:   resp.SandboxID,
		Name:        resp.Name,
		Description: resp.Description,
		CreatedAt:   resp.CreatedAt,
		SizeMB:      resp.SizeMB,
	}, nil
}

func (a *Adapter) ListSnapshots(ctx context.Context, sandboxID string) ([]*adapter.Snapshot, error) {
	infos, err := a.client.ListSnapshots(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	result := make([]*adapter.Snapshot, len(infos))
	for i, info := range infos {
		result[i] = &adapter.Snapshot{
			SnapshotID:  info.SnapshotID,
			SandboxID:   info.SandboxID,
			Name:        info.Name,
			Description: info.Description,
			CreatedAt:   info.CreatedAt,
			SizeMB:      info.SizeMB,
		}
	}
	return result, nil
}

// --- Ports ---

func (a *Adapter) ListPorts(ctx context.Context, sandboxID string) ([]*adapter.PortInfo, error) {
	resp, err := a.client.ListPorts(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	result := make([]*adapter.PortInfo, len(resp.Ports))
	for i, p := range resp.Ports {
		result[i] = &adapter.PortInfo{
			Port:  p.Port,
			Ready: p.Ready,
		}
	}
	return result, nil
}

func (a *Adapter) GetPortURL(ctx context.Context, sandboxID string, port int) (string, error) {
	resp, err := a.client.GetPortURL(ctx, sandboxID, port)
	if err != nil {
		return "", err
	}
	return resp.URL, nil
}

// --- Environment Variables ---

func (a *Adapter) SetEnvs(ctx context.Context, sandboxID string, envs map[string]string) error {
	return a.client.SetEnvs(ctx, sandboxID, envs)
}

// --- Logs ---

func (a *Adapter) GetLogs(ctx context.Context, sandboxID string) ([]*adapter.LogEntry, error) {
	logs, err := a.client.GetLogs(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	result := make([]*adapter.LogEntry, len(logs))
	for i, l := range logs {
		result[i] = &adapter.LogEntry{
			Timestamp: l.Timestamp,
			Message:   l.Message,
			Level:     l.Level,
			Source:    l.Source,
		}
	}
	return result, nil
}

// --- File Move ---

func (a *Adapter) MoveFile(ctx context.Context, sandboxID string, src, dst string) error {
	return a.client.MoveFile(ctx, sandboxID, src, dst)
}

// --- Template Tags ---

func (a *Adapter) CreateTag(ctx context.Context, templateID string, req *adapter.TagRequest) (*adapter.Tag, error) {
	resp, err := a.client.CreateTag(ctx, templateID, &dto.CreateTagRequest{
		Name:    req.Name,
		BuildID: req.BuildID,
	})
	if err != nil {
		return nil, err
	}
	return &adapter.Tag{
		Name:       resp.Name,
		TemplateID: templateID,
		BuildID:    resp.BuildID,
		CreatedAt:  resp.CreatedAt,
	}, nil
}

func (a *Adapter) ListTags(ctx context.Context, templateID string) ([]*adapter.Tag, error) {
	resp, err := a.client.ListTags(ctx, templateID)
	if err != nil {
		return nil, err
	}
	result := make([]*adapter.Tag, len(resp))
	for i, t := range resp {
		result[i] = &adapter.Tag{
			Name:       t.Name,
			TemplateID: templateID,
			BuildID:    t.BuildID,
			CreatedAt:  t.CreatedAt,
		}
	}
	return result, nil
}

func (a *Adapter) DeleteTag(ctx context.Context, templateID, tagName string) error {
	return a.client.DeleteTag(ctx, templateID, tagName)
}

// --- envd Data Plane ---

func (a *Adapter) GetEnvdEndpoint(ctx context.Context, sandboxID string) (string, string, error) {
	// E2B Cloud sandboxes already run envd; the SDK connects directly.
	// We return empty strings so the gateway doesn't proxy — the SDK
	// constructs the envd URL from sandboxDomain returned by the API.
	return "", "", fmt.Errorf("e2b-cloud adapter: SDK connects to envd directly via sandboxDomain")
}

// --- Access Token ---

func (a *Adapter) GetAccessToken(ctx context.Context, sandboxID string) (*adapter.AccessToken, error) {
	resp, err := a.client.GetAccessToken(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	return &adapter.AccessToken{
		Token:     resp.AccessToken,
		ExpiresAt: resp.ExpiresAt,
	}, nil
}

// --- Translation Helpers ---

func dtoToSandbox(info *dto.SandboxInfo, backend string) *adapter.Sandbox {
	status := adapter.SandboxStatusRunning
	switch info.State {
	case "running":
		status = adapter.SandboxStatusRunning
	case "paused", "pausing":
		status = adapter.SandboxStatusPaused
	case "stopped":
		status = adapter.SandboxStatusStopped
	case "error":
		status = adapter.SandboxStatusError
	}

	return &adapter.Sandbox{
		SandboxID:  info.SandboxID,
		TemplateID: info.TemplateID,
		Alias:      info.Alias,
		StartedAt:  info.StartedAt,
		EndAt:      info.EndAt,
		Status:     status,
		Metadata:   info.Metadata,
		ClientID:   info.ClientID,
		Backend:    backend,
	}
}

func dtoToTemplate(info *dto.TemplateInfo) *adapter.Template {
	name := info.TemplateID
	if len(info.Aliases) > 0 {
		name = info.Aliases[0]
	}

	return &adapter.Template{
		TemplateID: info.TemplateID,
		Name:       name,
		CPUCount:   info.CPUCount,
		MemoryMB:   info.MemoryMB,
		Public:     info.Public,
		BuildID:    info.BuildID,
		CreatedAt:  info.CreatedAt,
	}
}
