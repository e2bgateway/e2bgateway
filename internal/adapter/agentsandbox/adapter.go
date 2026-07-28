// Package agentsandbox implements the E2BGateway adapter for kubernetes-sigs/agent-sandbox.
// It uses the official agent-sandbox Go client (sigs.k8s.io/agent-sandbox/clients/go/sandbox)
// for lifecycle and data-plane operations, and the extensions API for pause/resume.
//
// Architecture:
//   - Control plane: official sandbox.Client manages SandboxClaim lifecycle
//   - Data plane: sandbox.Handle (Run, Read, Write, List) talks to in-pod runtime sidecar
//   - Pause/Resume: direct K8s API patch on Sandbox.spec.operatingMode
//   - E2B ID mapping: in-memory map + SandboxClaim annotation
package agentsandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"sigs.k8s.io/agent-sandbox/clients/go/sandbox"

	// Official CRD types
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
)

// Adapter implements adapter.SandboxAdapter using the official agent-sandbox client.
type Adapter struct {
	name      string
	namespace string

	// Official client manages SandboxClaim lifecycle and data-plane connections.
	client *sandbox.Client

	// K8s helper for direct API access (pause/resume, status queries).
	k8s *sandbox.K8sHelper

	// E2B sandbox ID → claim metadata.
	idMap   map[string]*sandboxEntry
	idMapMu sync.RWMutex

	// Template ID → Warm Pool name mapping.
	warmPoolMap   map[string]string
	warmPoolMapMu sync.RWMutex
}

// sandboxEntry stores metadata for an active sandbox.
type sandboxEntry struct {
	claimName  string
	templateID string
	createdAt  time.Time
	metadata   map[string]string
}

// AdapterConfig holds configuration for the agent-sandbox adapter.
type AdapterConfig struct {
	Name             string
	Namespace        string
	RestConfig       *rest.Config
	GatewayName      string
	GatewayNamespace string
	APIURL           string
	// WarmPoolName is the name of the SandboxWarmPool resource. Required.
	WarmPoolName string
	// TemplateToWarmPool maps E2B template IDs to agent-sandbox warm pool names.
	TemplateToWarmPool map[string]string
}

// New creates a new agent-sandbox adapter.
func New(cfg AdapterConfig) (*Adapter, error) {
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}
	if cfg.GatewayNamespace == "" {
		cfg.GatewayNamespace = "default"
	}

	k8s, err := sandbox.NewK8sHelper(cfg.RestConfig, logr.Discard())
	if err != nil {
		return nil, fmt.Errorf("creating k8s helper: %w", err)
	}

	opts := sandbox.Options{
		WarmPoolName:     cfg.WarmPoolName,
		Namespace:        cfg.Namespace,
		GatewayName:      cfg.GatewayName,
		GatewayNamespace: cfg.GatewayNamespace,
		APIURL:           cfg.APIURL,
		K8sHelper:        k8s,
		Quiet:            true,
	}

	client, err := sandbox.NewClient(context.Background(), opts)
	if err != nil {
		return nil, fmt.Errorf("creating sandbox client: %w", err)
	}

	warmPoolMap := make(map[string]string)
	for k, v := range cfg.TemplateToWarmPool {
		warmPoolMap[k] = v
	}

	return &Adapter{
		name:        cfg.Name,
		namespace:   cfg.Namespace,
		client:      client,
		k8s:         k8s,
		idMap:       make(map[string]*sandboxEntry),
		warmPoolMap: warmPoolMap,
	}, nil
}

// Name returns the adapter name.
func (a *Adapter) Name() string { return a.name }

// HealthCheck verifies connectivity.
func (a *Adapter) HealthCheck(ctx context.Context) error {
	_, err := a.client.ListAllSandboxes(ctx, a.namespace)
	return err
}

// --- Sandbox Lifecycle ---

// CreateSandbox provisions a new sandbox instance from the specified template.
func (a *Adapter) CreateSandbox(ctx context.Context, req *adapter.CreateSandboxRequest) (*adapter.Sandbox, error) {
	warmPool := a.resolveWarmPool(req.TemplateID)

	sb, err := a.client.CreateSandbox(ctx, warmPool, a.namespace)
	if err != nil {
		return nil, fmt.Errorf("creating sandbox: %w", err)
	}

	e2bID := generateE2BID()

	a.idMapMu.Lock()
	a.idMap[e2bID] = &sandboxEntry{
		claimName:  sb.ClaimName(),
		templateID: req.TemplateID,
		createdAt:  time.Now(),
		metadata:   req.Metadata,
	}
	a.idMapMu.Unlock()

	return &adapter.Sandbox{
		SandboxID:  e2bID,
		TemplateID: req.TemplateID,
		Status:     adapter.SandboxStatusRunning,
		StartedAt:  time.Now(),
		Metadata:   req.Metadata,
		Backend:    a.name,
	}, nil
}

// ListSandboxes returns all running sandboxes in the namespace.
func (a *Adapter) ListSandboxes(ctx context.Context, opts adapter.ListOptions) ([]*adapter.Sandbox, error) {
	claims, err := a.client.ListAllSandboxes(ctx, a.namespace)
	if err != nil {
		return nil, fmt.Errorf("listing sandboxes: %w", err)
	}

	a.idMapMu.RLock()
	defer a.idMapMu.RUnlock()

	// Build reverse map: claimName → e2bID
	claimToID := make(map[string]string, len(a.idMap))
	for id, e := range a.idMap {
		claimToID[e.claimName] = id
	}

	var result []*adapter.Sandbox
	for _, claimName := range claims {
		e2bID := claimToID[claimName]
		if e2bID == "" {
			e2bID = claimName
		}

		entry := a.idMap[e2bID]
		sbx := &adapter.Sandbox{
			SandboxID: e2bID,
			Status:    adapter.SandboxStatusRunning,
			Backend:   a.name,
		}
		if entry != nil {
			sbx.TemplateID = entry.templateID
			sbx.StartedAt = entry.createdAt
			sbx.Metadata = entry.metadata
		}
		result = append(result, sbx)
	}
	return result, nil
}

// GetSandbox retrieves details of a specific sandbox by ID.
func (a *Adapter) GetSandbox(ctx context.Context, sandboxID string) (*adapter.Sandbox, error) {
	claimName, err := a.resolveClaimName(sandboxID)
	if err != nil {
		return nil, err
	}

	sb, err := a.client.GetSandbox(ctx, claimName, a.namespace)
	if err != nil {
		return nil, fmt.Errorf("getting sandbox: %w", err)
	}

	a.idMapMu.RLock()
	entry := a.idMap[sandboxID]
	a.idMapMu.RUnlock()

	status := adapter.SandboxStatusStarting
	if sb.IsReady() {
		status = adapter.SandboxStatusRunning
	}

	result := &adapter.Sandbox{
		SandboxID: sandboxID,
		Status:    status,
		Backend:   a.name,
	}
	if entry != nil {
		result.TemplateID = entry.templateID
		result.StartedAt = entry.createdAt
		result.Metadata = entry.metadata
	}
	return result, nil
}

// KillSandbox terminates and destroys a sandbox instance.
func (a *Adapter) KillSandbox(ctx context.Context, sandboxID string) error {
	claimName, err := a.resolveClaimName(sandboxID)
	if err != nil {
		return err
	}
	if err := a.client.DeleteSandbox(ctx, claimName, a.namespace); err != nil {
		return fmt.Errorf("deleting sandbox: %w", err)
	}
	a.idMapMu.Lock()
	delete(a.idMap, sandboxID)
	a.idMapMu.Unlock()
	return nil
}

// PauseSandbox suspends a sandbox, preserving its state for later resumption.
func (a *Adapter) PauseSandbox(ctx context.Context, sandboxID string) error {
	sandboxName, err := a.resolveSandboxCRName(ctx, sandboxID)
	if err != nil {
		return err
	}
	patch := []byte(`{"spec":{"operatingMode":"Suspended"}}`)
	_, err = a.k8s.AgentsClient.Sandboxes(a.namespace).Patch(
		ctx, sandboxName, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	return err
}

// ResumeSandbox restores a previously paused sandbox to running state.
func (a *Adapter) ResumeSandbox(ctx context.Context, sandboxID string) (*adapter.Sandbox, error) {
	sandboxName, err := a.resolveSandboxCRName(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	patch := []byte(`{"spec":{"operatingMode":"Running"}}`)
	_, err = a.k8s.AgentsClient.Sandboxes(a.namespace).Patch(
		ctx, sandboxName, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if err != nil {
		return nil, err
	}
	return a.GetSandbox(ctx, sandboxID)
}

// SetTimeout updates the sandbox's auto-termination timer.
func (a *Adapter) SetTimeout(ctx context.Context, sandboxID string, timeout time.Duration) error {
	claimName, err := a.resolveClaimName(sandboxID)
	if err != nil {
		return err
	}
	shutdownTime := metav1.NewTime(time.Now().Add(timeout))
	tsJSON, _ := json.Marshal(shutdownTime)
	patch := []byte(fmt.Sprintf(`{"spec":{"lifecycle":{"shutdownTime":%s}}}`, tsJSON))
	_, err = a.k8s.ExtensionsClient.SandboxClaims(a.namespace).Patch(
		ctx, claimName, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	return err
}

// --- Code Execution ---

// ExecuteCode runs code in the sandbox and returns the results synchronously.
func (a *Adapter) ExecuteCode(ctx context.Context, sandboxID string, req *adapter.CodeExecutionRequest) (*adapter.CodeExecutionResult, error) {
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	command := wrapCodeInCommand(req.Code, req.Language)
	result, err := handle.Run(ctx, command)
	if err != nil {
		return nil, fmt.Errorf("executing code: %w", err)
	}
	return &adapter.CodeExecutionResult{
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		ExitCode: result.ExitCode,
	}, nil
}

// ExecuteCodeStream runs code and streams output via the provided stream.
func (a *Adapter) ExecuteCodeStream(ctx context.Context, sandboxID string, req *adapter.CodeExecutionRequest, stream adapter.CodeStream) error {
	result, err := a.ExecuteCode(ctx, sandboxID, req)
	if err != nil {
		return stream.Send(&adapter.StreamMessage{Type: "error", Data: err.Error()})
	}
	if result.Stdout != "" {
		_ = stream.Send(&adapter.StreamMessage{Type: "stdout", Data: result.Stdout})
	}
	if result.Stderr != "" {
		_ = stream.Send(&adapter.StreamMessage{Type: "stderr", Data: result.Stderr})
	}
	return stream.Send(&adapter.StreamMessage{
		Type: "result",
		Data: map[string]interface{}{"exitCode": result.ExitCode},
	})
}

// RunCommand executes a shell command in the sandbox.
func (a *Adapter) RunCommand(ctx context.Context, sandboxID string, req *adapter.CommandRequest) (*adapter.CommandResult, error) {
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	command := req.Command
	if len(req.Args) > 0 {
		command = command + " " + strings.Join(req.Args, " ")
	}
	result, err := handle.Run(ctx, command)
	if err != nil {
		return nil, fmt.Errorf("running command: %w", err)
	}
	return &adapter.CommandResult{
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		ExitCode: result.ExitCode,
	}, nil
}

// --- Filesystem ---

// WriteFile writes content to a file in the sandbox.
func (a *Adapter) WriteFile(ctx context.Context, sandboxID string, req *adapter.FileWriteRequest) error {
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return err
	}
	return handle.Write(ctx, req.Path, req.Content)
}

// ReadFile reads file content from the sandbox.
func (a *Adapter) ReadFile(ctx context.Context, sandboxID string, path string) (*adapter.FileContent, error) {
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	data, err := handle.Read(ctx, path)
	if err != nil {
		return nil, err
	}
	return &adapter.FileContent{Path: path, Content: data, Size: int64(len(data))}, nil
}

// UploadFile uploads a file to the sandbox.
func (a *Adapter) UploadFile(ctx context.Context, sandboxID string, req *adapter.FileUploadRequest) error {
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(req.Reader)
	if err != nil {
		return err
	}
	return handle.Write(ctx, req.Path, data)
}

// DownloadFile downloads a file from the sandbox.
func (a *Adapter) DownloadFile(ctx context.Context, sandboxID string, path string) (io.ReadCloser, error) {
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	data, err := handle.Read(ctx, path)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader(string(data))), nil
}

// ListFiles lists files in a directory within the sandbox.
func (a *Adapter) ListFiles(ctx context.Context, sandboxID string, path string) ([]adapter.FileInfo, error) {
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	entries, err := handle.List(ctx, path)
	if err != nil {
		return nil, err
	}
	var result []adapter.FileInfo
	for _, e := range entries {
		result = append(result, adapter.FileInfo{
			Name:  e.Name,
			Path:  path + "/" + e.Name,
			Size:  e.Size,
			IsDir: e.Type == sandbox.FileTypeDirectory,
		})
	}
	return result, nil
}

// MakeDir creates a directory in the sandbox.
func (a *Adapter) MakeDir(ctx context.Context, sandboxID string, path string) error {
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return err
	}
	_, err = handle.Run(ctx, "mkdir -p "+path)
	return err
}

// RemoveFile removes a file or directory from the sandbox.
func (a *Adapter) RemoveFile(ctx context.Context, sandboxID string, path string) error {
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return err
	}
	_, err = handle.Run(ctx, "rm -rf "+path)
	return err
}

// --- Templates ---

// ListTemplates returns available sandbox templates.
func (a *Adapter) ListTemplates(ctx context.Context, opts adapter.ListOptions) ([]*adapter.Template, error) {
	list, err := a.k8s.ExtensionsClient.SandboxTemplates(a.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing templates: %w", err)
	}
	var result []*adapter.Template
	for i := range list.Items {
		result = append(result, templateToDomain(&list.Items[i]))
	}
	return result, nil
}

// GetTemplate retrieves details of a specific template by ID.
func (a *Adapter) GetTemplate(ctx context.Context, templateID string) (*adapter.Template, error) {
	t, err := a.k8s.ExtensionsClient.SandboxTemplates(a.namespace).Get(ctx, templateID, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting template: %w", err)
	}
	return templateToDomain(t), nil
}

// --- Internal helpers ---

func (a *Adapter) resolveWarmPool(templateID string) string {
	a.warmPoolMapMu.RLock()
	defer a.warmPoolMapMu.RUnlock()
	if wp, ok := a.warmPoolMap[templateID]; ok {
		return wp
	}
	return templateID
}

func (a *Adapter) resolveClaimName(sandboxID string) (string, error) {
	a.idMapMu.RLock()
	defer a.idMapMu.RUnlock()
	if entry, ok := a.idMap[sandboxID]; ok {
		return entry.claimName, nil
	}
	return sandboxID, nil // fallback: ID is claim name
}

func (a *Adapter) resolveSandboxCRName(ctx context.Context, sandboxID string) (string, error) {
	claimName, err := a.resolveClaimName(sandboxID)
	if err != nil {
		return "", err
	}
	claim, err := a.k8s.ExtensionsClient.SandboxClaims(a.namespace).Get(ctx, claimName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting claim: %w", err)
	}
	if claim.Status.SandboxStatus.Name == "" {
		return "", fmt.Errorf("sandbox not yet bound to claim %s", claimName)
	}
	return claim.Status.SandboxStatus.Name, nil
}

func (a *Adapter) getHandle(ctx context.Context, sandboxID string) (sandbox.Handle, error) {
	claimName, err := a.resolveClaimName(sandboxID)
	if err != nil {
		return nil, err
	}
	sb, err := a.client.GetSandbox(ctx, claimName, a.namespace)
	if err != nil {
		return nil, fmt.Errorf("getting sandbox handle: %w", err)
	}
	if !sb.IsReady() {
		if err := sb.Open(ctx); err != nil {
			return nil, fmt.Errorf("opening sandbox connection: %w", err)
		}
	}
	return sb, nil
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

func templateToDomain(t *extv1beta1.SandboxTemplate) *adapter.Template {
	return &adapter.Template{
		TemplateID: t.Name,
		Name:       t.Name,
		CreatedAt:  t.CreationTimestamp.Time,
	}
}

// --- Template Create/Delete ---

func (a *Adapter) CreateTemplate(_ context.Context, _ *adapter.CreateTemplateRequest) (*adapter.TemplateBuild, error) {
	return nil, fmt.Errorf("create template not supported by agent-sandbox backend")
}

func (a *Adapter) DeleteTemplate(_ context.Context, _ string) error {
	return fmt.Errorf("delete template not supported by agent-sandbox backend")
}

// --- Template Builds ---

func (a *Adapter) TriggerBuild(_ context.Context, _ string, _ *adapter.BuildRequest) (*adapter.TemplateBuild, error) {
	return nil, fmt.Errorf("trigger build not supported by agent-sandbox backend")
}

func (a *Adapter) GetBuildStatus(_ context.Context, _, _ string) (*adapter.BuildStatus, error) {
	return nil, fmt.Errorf("get build status not supported by agent-sandbox backend")
}

// --- Template Aliases ---

func (a *Adapter) CreateAlias(_ context.Context, _ string, _ string) error {
	return fmt.Errorf("create alias not supported by agent-sandbox backend")
}

func (a *Adapter) DeleteAlias(_ context.Context, _, _ string) error {
	return fmt.Errorf("delete alias not supported by agent-sandbox backend")
}

// --- Warm Pools ---

func (a *Adapter) ListWarmPools(_ context.Context) ([]*adapter.WarmPool, error) {
	// Agent-sandbox manages warm pools via SandboxTemplate resources
	// Return empty list as warm pools are managed differently
	return []*adapter.WarmPool{}, nil
}

func (a *Adapter) CreateWarmPool(_ context.Context, _ *adapter.WarmPoolCreateRequest) (*adapter.WarmPool, error) {
	return nil, fmt.Errorf("create warm pool not supported by agent-sandbox backend")
}

func (a *Adapter) GetWarmPool(_ context.Context, _ string) (*adapter.WarmPool, error) {
	return nil, fmt.Errorf("get warm pool not supported by agent-sandbox backend")
}

func (a *Adapter) DeleteWarmPool(_ context.Context, _ string) error {
	return fmt.Errorf("delete warm pool not supported by agent-sandbox backend")
}

func (a *Adapter) UpdateWarmPoolSize(_ context.Context, _ string, _ int) error {
	return fmt.Errorf("update warm pool size not supported by agent-sandbox backend")
}

// --- Processes ---

func (a *Adapter) ListProcesses(ctx context.Context, sandboxID string) ([]*adapter.ProcessInfo, error) {
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	result, err := handle.Run(ctx, "ps aux --no-headers")
	if err != nil {
		return nil, fmt.Errorf("listing processes: %w", err)
	}
	var processes []*adapter.ProcessInfo
	for i, line := range strings.Split(result.Stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		processes = append(processes, &adapter.ProcessInfo{
			ProcessID: fmt.Sprintf("proc-%d", i),
			Command:   strings.TrimSpace(line),
			PID:       i,
			Status:    "running",
			StartedAt: time.Now(),
		})
	}
	return processes, nil
}

func (a *Adapter) KillProcess(ctx context.Context, sandboxID, processID string) error {
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return err
	}
	_, err = handle.Run(ctx, fmt.Sprintf("kill -9 %s 2>/dev/null || true", processID))
	return err
}

func (a *Adapter) SendStdin(_ context.Context, _, _ string, _ string) error {
	return fmt.Errorf("send stdin not supported by agent-sandbox backend")
}

// --- Snapshots ---

func (a *Adapter) CreateSnapshot(_ context.Context, _ string, _ *adapter.SnapshotRequest) (*adapter.Snapshot, error) {
	return nil, fmt.Errorf("create snapshot not supported by agent-sandbox backend")
}

func (a *Adapter) ListSnapshots(_ context.Context, _ string) ([]*adapter.Snapshot, error) {
	return []*adapter.Snapshot{}, nil
}

// --- Ports ---

func (a *Adapter) ListPorts(_ context.Context, _ string) ([]*adapter.PortInfo, error) {
	return []*adapter.PortInfo{}, nil
}

func (a *Adapter) GetPortURL(_ context.Context, _ string, _ int) (string, error) {
	return "", fmt.Errorf("get port URL not supported by agent-sandbox backend")
}

// --- Access Token ---

func (a *Adapter) GetAccessToken(_ context.Context, _ string) (*adapter.AccessToken, error) {
	return nil, fmt.Errorf("get access token not supported by agent-sandbox backend")
}

// --- Environment Variables ---

func (a *Adapter) SetEnvs(ctx context.Context, sandboxID string, envs map[string]string) error {
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return err
	}
	for k, v := range envs {
		_, err := handle.Run(ctx, fmt.Sprintf("export %s=%q", k, v))
		if err != nil {
			return err
		}
	}
	return nil
}

// --- Logs ---

func (a *Adapter) GetLogs(_ context.Context, _ string) ([]*adapter.LogEntry, error) {
	return []*adapter.LogEntry{}, nil
}

// --- File Move ---

func (a *Adapter) MoveFile(ctx context.Context, sandboxID string, src, dst string) error {
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return err
	}
	_, err = handle.Run(ctx, fmt.Sprintf("mv %q %q", src, dst))
	return err
}

// --- Template Tags ---

func (a *Adapter) CreateTag(_ context.Context, _ string, _ *adapter.TagRequest) (*adapter.Tag, error) {
	return nil, fmt.Errorf("create tag not supported by agent-sandbox backend")
}

func (a *Adapter) ListTags(_ context.Context, _ string) ([]*adapter.Tag, error) {
	return []*adapter.Tag{}, nil
}

func (a *Adapter) DeleteTag(_ context.Context, _ string, _ string) error {
	return fmt.Errorf("delete tag not supported by agent-sandbox backend")
}

// --- envd Data Plane ---

// GetEnvdEndpoint returns the envd endpoint for a sandbox pod.
// The sandbox container must have envd running on port 49983.
// It resolves the pod IP via the K8s API and returns http://{podIP}:49983.
func (a *Adapter) GetEnvdEndpoint(ctx context.Context, sandboxID string) (string, string, error) {
	podName, err := a.resolveSandboxCRName(ctx, sandboxID)
	if err != nil {
		return "", "", fmt.Errorf("resolving sandbox pod for envd endpoint (sandbox %q): %w", sandboxID, err)
	}

	pod, err := a.k8s.CoreClient.Pods(a.namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", "", fmt.Errorf("getting pod %q for envd endpoint: %w", podName, err)
	}

	if pod.Status.PodIP == "" {
		return "", "", fmt.Errorf("pod %q has no IP yet (phase=%s)", podName, pod.Status.Phase)
	}

	return fmt.Sprintf("http://%s:49983", pod.Status.PodIP), "", nil
}

// generateE2BID generates an E2B-compatible sandbox ID (12 hex chars).
func generateE2BID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
