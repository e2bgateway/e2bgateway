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

package agentsandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	"sigs.k8s.io/agent-sandbox/clients/go/sandbox"
	// Official CRD types.
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/adapter/util"
	"github.com/e2bgateway/e2bgateway/internal/cache"
	"github.com/e2bgateway/e2bgateway/internal/envd"
)

// envsToEnvVarList converts a map of environment variables to the CRD EnvVar
// slice injected into SandboxClaim.Spec.Env at claim creation time.
func envsToEnvVarList(envs map[string]string) []extv1beta1.EnvVar {
	if len(envs) == 0 {
		return nil
	}
	vars := make([]extv1beta1.EnvVar, 0, len(envs))
	for k, v := range envs {
		vars = append(vars, extv1beta1.EnvVar{Name: k, Value: v})
	}
	return vars
}

// optionsWithEnv derives per-request Options from the base options for
// sandboxes created with environment variables. The base is copied, never
// mutated (the adapter shares it across concurrent requests).
func optionsWithEnv(base sandbox.Options, warmPool, namespace string, envs map[string]string) sandbox.Options {
	opts := base
	opts.WarmPoolName = warmPool
	opts.Namespace = namespace
	opts.Env = envsToEnvVarList(envs)
	return opts
}

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

	// Token cache for access token generation and validation.
	tokenCache *cache.Cache

	// portTracker tracks opened ports per sandbox for ListPorts.
	// Key: sandboxID, Value: map[int]bool (port -> ready).
	portTracker   map[string]map[int]bool
	portTrackerMu sync.RWMutex

	// envdClients caches per-sandbox envd clients for data plane operations.
	// Entries expire after envdClientTTL so that pod IP changes (reschedules)
	// are eventually picked up, and dead-sandbox entries are garbage-collected.
	// Key: sandboxID, Value: *envdClientEntry
	envdClients   map[string]*envdClientEntry
	envdClientsMu sync.RWMutex

	// useEnvdDataPlane controls whether to use envd ConnectRPC for data plane
	// operations (default: true). Set to false to use agent-sandbox SDK handle
	// (requires agent-sandbox runtime sidecar in the pod).
	useEnvdDataPlane bool

	// registry provides access to the sandbox→backend mapping.
	registry *adapter.Registry

	// baseOpts stores the client-level Options from construction. Per-request
	// env injection derives a copy of these options when Envs are present.
	baseOpts sandbox.Options
}

// sandboxEntry stores metadata for an active sandbox.
type sandboxEntry struct {
	claimName  string
	templateID string
	createdAt  time.Time
	metadata   map[string]string
}

// envdClientEntry wraps an envd.Client with a creation timestamp so stale
// entries (e.g., after pod reschedule to a different IP) can be expired.
type envdClientEntry struct {
	client    *envd.Client
	createdAt time.Time
}

// envdClientTTL is the maximum age of a cached envd client before it is
// refreshed from the sandbox metadata. 5 minutes balances responsiveness to
// pod IP changes against the cost of re-resolving the envd endpoint.
const envdClientTTL = 5 * time.Minute

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
	// UseEnvdDataPlane controls whether to use envd ConnectRPC for data plane
	// operations (default: true). Set to false to use agent-sandbox SDK handle.
	UseEnvdDataPlane bool
	// Registry provides access to the sandbox→backend mapping.
	Registry *adapter.Registry
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
	maps.Copy(warmPoolMap, cfg.TemplateToWarmPool)

	return &Adapter{
		name:             cfg.Name,
		namespace:        cfg.Namespace,
		client:           client,
		k8s:              k8s,
		idMap:            make(map[string]*sandboxEntry),
		warmPoolMap:      warmPoolMap,
		tokenCache:       cache.New(10000, 1*time.Hour),
		portTracker:      make(map[string]map[int]bool),
		envdClients:      make(map[string]*envdClientEntry),
		useEnvdDataPlane: cfg.UseEnvdDataPlane,
		registry:         cfg.Registry,
		baseOpts:         opts,
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

	// When envs are requested, derive per-request options and create the
	// SandboxClaim with Spec.Env so the container starts with the variables.
	// NOTE: the SDK cold-starts the sandbox from the warm pool template in
	// this case (the pre-warmed pod cannot be reused), which is slower.
	// This is inherent SDK behavior, not a gateway regression.
	var sb *sandbox.Sandbox
	if len(req.Envs) > 0 {
		handle, err := sandbox.New(ctx, optionsWithEnv(a.baseOpts, warmPool, a.namespace, req.Envs))
		if err != nil {
			return nil, fmt.Errorf("creating sandbox with envs: %w", err)
		}
		if err := handle.Open(ctx); err != nil {
			return nil, fmt.Errorf("opening sandbox with envs: %w", err)
		}
		sb = handle
	} else {
		var err error
		sb, err = a.client.CreateSandbox(ctx, warmPool, a.namespace)
		if err != nil {
			return nil, fmt.Errorf("creating sandbox: %w", err)
		}
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

	// Register sandbox in the sandbox→backend mapping
	if a.registry != nil {
		a.registry.SandboxBackend().Set(e2bID, a.name)
	}

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
	a.portTrackerMu.Lock()
	delete(a.portTracker, sandboxID)
	a.portTrackerMu.Unlock()
	// Clean up envd client cache
	a.envdClientsMu.Lock()
	delete(a.envdClients, sandboxID)
	a.envdClientsMu.Unlock()
	// Unregister sandbox from the sandbox→backend mapping
	if a.registry != nil {
		a.registry.SandboxBackend().Delete(sandboxID)
	}
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
	patch := fmt.Appendf(nil, `{"spec":{"lifecycle":{"shutdownTime":%s}}}`, tsJSON)
	_, err = a.k8s.ExtensionsClient.SandboxClaims(a.namespace).Patch(
		ctx, claimName, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	return err
}

// --- Code Execution ---

// ExecuteCode runs code in the sandbox and returns the results synchronously.
func (a *Adapter) ExecuteCode(ctx context.Context, sandboxID string, req *adapter.CodeExecutionRequest) (*adapter.CodeExecutionResult, error) {
	if a.useEnvdDataPlane {
		return a.executeCodeViaEnvd(ctx, sandboxID, req)
	}
	return a.executeCodeViaHandle(ctx, sandboxID, req)
}

// executeCodeViaEnvd executes code using envd ConnectRPC.
func (a *Adapter) executeCodeViaEnvd(ctx context.Context, sandboxID string, req *adapter.CodeExecutionRequest) (*adapter.CodeExecutionResult, error) {
	envdClient, err := a.getOrCreateEnvdClient(ctx, sandboxID)
	if err != nil {
		return nil, err
	}

	command := util.WrapCodeInCommand(req.Code, req.Language)
	stdout, stderr, exitCode, err := envdClient.RunCommand(ctx, command, req.Cwd, req.EnvVars)
	if err != nil {
		return nil, fmt.Errorf("executing code via envd: %w", err)
	}

	return &adapter.CodeExecutionResult{
		Stdout:   stdout,
		Stderr:   stderr,
		ExitCode: int(exitCode),
	}, nil
}

// executeCodeViaHandle executes code using agent-sandbox SDK handle (legacy).
func (a *Adapter) executeCodeViaHandle(ctx context.Context, sandboxID string, req *adapter.CodeExecutionRequest) (*adapter.CodeExecutionResult, error) {
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	command := util.WrapCodeInCommand(req.Code, req.Language)
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
		Data: map[string]any{"exitCode": result.ExitCode},
	})
}

// RunCommand executes a shell command in the sandbox.
func (a *Adapter) RunCommand(ctx context.Context, sandboxID string, req *adapter.CommandRequest) (*adapter.CommandResult, error) {
	if a.useEnvdDataPlane {
		return a.runCommandViaEnvd(ctx, sandboxID, req)
	}
	return a.runCommandViaHandle(ctx, sandboxID, req)
}

// runCommandViaEnvd runs a command using envd ConnectRPC.
func (a *Adapter) runCommandViaEnvd(ctx context.Context, sandboxID string, req *adapter.CommandRequest) (*adapter.CommandResult, error) {
	envdClient, err := a.getOrCreateEnvdClient(ctx, sandboxID)
	if err != nil {
		return nil, err
	}

	command := req.Command
	if len(req.Args) > 0 {
		// Shell-escape each argument to prevent injection
		escapedArgs := make([]string, len(req.Args))
		for i, arg := range req.Args {
			escapedArgs[i] = util.ShellQuote(arg)
		}
		command = command + " " + strings.Join(escapedArgs, " ")
	}

	stdout, stderr, exitCode, err := envdClient.RunCommand(ctx, command, req.Cwd, req.EnvVars)
	if err != nil {
		return nil, fmt.Errorf("running command via envd: %w", err)
	}

	return &adapter.CommandResult{
		Stdout:   stdout,
		Stderr:   stderr,
		ExitCode: int(exitCode),
	}, nil
}

// runCommandViaHandle runs a command using agent-sandbox SDK handle (legacy).
func (a *Adapter) runCommandViaHandle(ctx context.Context, sandboxID string, req *adapter.CommandRequest) (*adapter.CommandResult, error) {
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	command := req.Command
	if len(req.Args) > 0 {
		// Shell-escape each argument to prevent injection
		escapedArgs := make([]string, len(req.Args))
		for i, arg := range req.Args {
			escapedArgs[i] = util.ShellQuote(arg)
		}
		command = command + " " + strings.Join(escapedArgs, " ")
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
	if a.useEnvdDataPlane {
		return a.writeFileViaEnvd(ctx, sandboxID, req)
	}
	return a.writeFileViaHandle(ctx, sandboxID, req)
}

// writeFileViaEnvd writes a file using envd REST API.
func (a *Adapter) writeFileViaEnvd(ctx context.Context, sandboxID string, req *adapter.FileWriteRequest) error {
	envdClient, err := a.getOrCreateEnvdClient(ctx, sandboxID)
	if err != nil {
		return err
	}
	return envdClient.UploadFile(ctx, req.Path, bytes.NewReader(req.Content))
}

// writeFileViaHandle writes a file using agent-sandbox SDK handle (legacy).
func (a *Adapter) writeFileViaHandle(ctx context.Context, sandboxID string, req *adapter.FileWriteRequest) error {
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return err
	}
	return handle.Write(ctx, req.Path, req.Content)
}

// ReadFile reads file content from the sandbox.
func (a *Adapter) ReadFile(ctx context.Context, sandboxID string, path string) (*adapter.FileContent, error) {
	if a.useEnvdDataPlane {
		return a.readFileViaEnvd(ctx, sandboxID, path)
	}
	return a.readFileViaHandle(ctx, sandboxID, path)
}

// readFileViaEnvd reads a file using envd REST API.
func (a *Adapter) readFileViaEnvd(ctx context.Context, sandboxID string, path string) (*adapter.FileContent, error) {
	envdClient, err := a.getOrCreateEnvdClient(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	reader, err := envdClient.DownloadFile(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return &adapter.FileContent{Path: path, Content: data, Size: int64(len(data))}, nil
}

// readFileViaHandle reads a file using agent-sandbox SDK handle (legacy).
func (a *Adapter) readFileViaHandle(ctx context.Context, sandboxID string, path string) (*adapter.FileContent, error) {
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
	if a.useEnvdDataPlane {
		return a.uploadFileViaEnvd(ctx, sandboxID, req)
	}
	return a.uploadFileViaHandle(ctx, sandboxID, req)
}

// uploadFileViaEnvd uploads a file using envd REST API.
func (a *Adapter) uploadFileViaEnvd(ctx context.Context, sandboxID string, req *adapter.FileUploadRequest) error {
	envdClient, err := a.getOrCreateEnvdClient(ctx, sandboxID)
	if err != nil {
		return err
	}
	return envdClient.UploadFile(ctx, req.Path, req.Reader)
}

// uploadFileViaHandle uploads a file using agent-sandbox SDK handle (legacy).
func (a *Adapter) uploadFileViaHandle(ctx context.Context, sandboxID string, req *adapter.FileUploadRequest) error {
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
	if a.useEnvdDataPlane {
		return a.downloadFileViaEnvd(ctx, sandboxID, path)
	}
	return a.downloadFileViaHandle(ctx, sandboxID, path)
}

// downloadFileViaEnvd downloads a file using envd REST API.
func (a *Adapter) downloadFileViaEnvd(ctx context.Context, sandboxID string, path string) (io.ReadCloser, error) {
	envdClient, err := a.getOrCreateEnvdClient(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	return envdClient.DownloadFile(ctx, path)
}

// downloadFileViaHandle downloads a file using agent-sandbox SDK handle (legacy).
func (a *Adapter) downloadFileViaHandle(ctx context.Context, sandboxID string, path string) (io.ReadCloser, error) {
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	data, err := handle.Read(ctx, path)
	if err != nil {
		return nil, err
	}
	// Return bytes directly to preserve binary data
	return io.NopCloser(bytes.NewReader(data)), nil
}

// ListFiles lists files in a directory within the sandbox.
func (a *Adapter) ListFiles(ctx context.Context, sandboxID string, path string) ([]adapter.FileInfo, error) {
	if a.useEnvdDataPlane {
		return a.listFilesViaEnvd(ctx, sandboxID, path)
	}
	return a.listFilesViaHandle(ctx, sandboxID, path)
}

// listFilesViaEnvd lists files using envd ConnectRPC.
func (a *Adapter) listFilesViaEnvd(ctx context.Context, sandboxID string, path string) ([]adapter.FileInfo, error) {
	envdClient, err := a.getOrCreateEnvdClient(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	entries, err := envdClient.ListDir(ctx, path, 1)
	if err != nil {
		return nil, err
	}
	var result []adapter.FileInfo
	for _, e := range entries {
		result = append(result, adapter.FileInfo{
			Name:  e.Name,
			Path:  e.Path,
			Size:  e.SizeInt64(),
			IsDir: e.IsDir(),
		})
	}
	return result, nil
}

// listFilesViaHandle lists files using agent-sandbox SDK handle (legacy).
func (a *Adapter) listFilesViaHandle(ctx context.Context, sandboxID string, path string) ([]adapter.FileInfo, error) {
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
	if a.useEnvdDataPlane {
		return a.makeDirViaEnvd(ctx, sandboxID, path)
	}
	return a.makeDirViaHandle(ctx, sandboxID, path)
}

// makeDirViaEnvd creates a directory using envd ConnectRPC.
func (a *Adapter) makeDirViaEnvd(ctx context.Context, sandboxID string, path string) error {
	envdClient, err := a.getOrCreateEnvdClient(ctx, sandboxID)
	if err != nil {
		return err
	}
	return envdClient.MakeDir(ctx, path)
}

// makeDirViaHandle creates a directory using agent-sandbox SDK handle (legacy).
func (a *Adapter) makeDirViaHandle(ctx context.Context, sandboxID string, path string) error {
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return err
	}
	_, err = handle.Run(ctx, "mkdir -p "+util.ShellQuote(path))
	return err
}

// RemoveFile removes a file or directory from the sandbox.
func (a *Adapter) RemoveFile(ctx context.Context, sandboxID string, path string) error {
	if a.useEnvdDataPlane {
		return a.removeFileViaEnvd(ctx, sandboxID, path)
	}
	return a.removeFileViaHandle(ctx, sandboxID, path)
}

// removeFileViaEnvd removes a file using envd ConnectRPC.
func (a *Adapter) removeFileViaEnvd(ctx context.Context, sandboxID string, path string) error {
	envdClient, err := a.getOrCreateEnvdClient(ctx, sandboxID)
	if err != nil {
		return err
	}
	return envdClient.Remove(ctx, path)
}

// removeFileViaHandle removes a file using agent-sandbox SDK handle (legacy).
func (a *Adapter) removeFileViaHandle(ctx context.Context, sandboxID string, path string) error {
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return err
	}
	_, err = handle.Run(ctx, "rm -rf "+util.ShellQuote(path))
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

// getOrCreateEnvdClient returns the envd client for a sandbox, creating one if needed.
// This is used when useEnvdDataPlane is true to communicate with envd directly via ConnectRPC.
// Entries older than envdClientTTL are evicted so pod IP changes are picked up.
func (a *Adapter) getOrCreateEnvdClient(ctx context.Context, sandboxID string) (*envd.Client, error) {
	// Fast path: check under read lock (and verify TTL)
	a.envdClientsMu.RLock()
	if entry, ok := a.envdClients[sandboxID]; ok && time.Since(entry.createdAt) < envdClientTTL {
		a.envdClientsMu.RUnlock()
		return entry.client, nil
	}
	a.envdClientsMu.RUnlock()

	// Slow path: resolve envd endpoint
	envdURL, token, err := a.GetEnvdEndpoint(ctx, sandboxID)
	if err != nil {
		return nil, fmt.Errorf("getting envd endpoint: %w", err)
	}

	ec := envd.NewClient(envd.ClientConfig{
		BaseURL:     envdURL,
		AccessToken: token,
		SandboxID:   sandboxID,
	})

	// Double-check locking
	a.envdClientsMu.Lock()
	defer a.envdClientsMu.Unlock()
	if existing, ok := a.envdClients[sandboxID]; ok && time.Since(existing.createdAt) < envdClientTTL {
		return existing.client, nil
	}
	a.envdClients[sandboxID] = &envdClientEntry{client: ec, createdAt: time.Now()}
	return ec, nil
}

func templateToDomain(t *extv1beta1.SandboxTemplate) *adapter.Template {
	tmpl := &adapter.Template{
		TemplateID: t.Name,
		Name:       t.Name,
		CreatedAt:  t.CreationTimestamp.Time,
		Metadata:   make(map[string]string),
	}

	// Extract buildID from annotations (set during CreateTemplate).
	if t.Annotations != nil {
		if bid, ok := t.Annotations[annotationBuildID]; ok {
			tmpl.BuildID = bid
		}
		// Preserve user-defined metadata stored as annotations.
		for k, v := range t.Annotations {
			if key, ok := strings.CutPrefix(k, annotationMetadataPrefix); ok {
				tmpl.Metadata[key] = v
			}
		}
	}

	// Extract CPU/memory from the first container's resource requests.
	if len(t.Spec.PodTemplate.Spec.Containers) > 0 {
		res := t.Spec.PodTemplate.Spec.Containers[0].Resources
		if cpuQ, ok := res.Requests["cpu"]; ok {
			// Convert to whole cores (round up to at least 1 if any is set).
			tmpl.CPUCount = max(1, int(cpuQ.Value()))
			if cpuQ.MilliValue() > 0 && cpuQ.Value() == 0 {
				tmpl.CPUCount = 1
			}
		}
		if memQ, ok := res.Requests["memory"]; ok {
			tmpl.MemoryMB = int(memQ.Value() / (1024 * 1024))
		}
	}

	return tmpl
}

// Annotation keys used on SandboxTemplate CRDs to store E2B-level metadata.
const (
	// annotationBuildID stores the synthetic build ID for the template.
	annotationBuildID = "e2bgateway.io/build-id"
	// annotationDockerfile stores the Dockerfile contents from CreateTemplate.
	annotationDockerfile = "e2bgateway.io/dockerfile"
	// annotationStartCmd stores the start command from CreateTemplate.
	annotationStartCmd = "e2bgateway.io/start-cmd"
	// annotationAliases stores comma-separated aliases for the template.
	annotationAliases = "e2bgateway.io/aliases"
	// annotationMetadataPrefix is the prefix for user-defined metadata annotations.
	annotationMetadataPrefix = "e2bgateway.io/meta-"
)

// --- Template Create/Delete ---

// CreateTemplate creates a new SandboxTemplate CRD in the configured namespace.
// The CRD encapsulates the E2B template definition; since agent-sandbox manages
// templates as Kubernetes resources, the build completes synchronously (the
// returned BuildID is synthetic).
func (a *Adapter) CreateTemplate(ctx context.Context, req *adapter.CreateTemplateRequest) (*adapter.TemplateBuild, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("template name is required")
	}

	// Generate a unique, DNS-1123-safe template ID.
	templateID := generateTemplateID(req.Name)
	buildID := "build-" + generateE2BID()

	// Convert CPU/memory to Kubernetes resource quantities.
	cpuQuantity := "500m"
	memQuantity := "512Mi"
	if req.CPUCount > 0 {
		cpuQuantity = fmt.Sprintf("%d", req.CPUCount)
	}
	if req.MemoryMB > 0 {
		memQuantity = fmt.Sprintf("%dMi", req.MemoryMB)
	}

	annotations := map[string]string{
		annotationBuildID: buildID,
	}
	if req.Dockerfile != "" {
		annotations[annotationDockerfile] = req.Dockerfile
	}
	if req.StartCmd != "" {
		annotations[annotationStartCmd] = req.StartCmd
	}

	tmpl := &extv1beta1.SandboxTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:        templateID,
			Namespace:   a.namespace,
			Annotations: annotations,
		},
		Spec: extv1beta1.SandboxTemplateSpec{
			SandboxBlueprint: sandboxv1beta1.SandboxBlueprint{
				PodTemplate: sandboxv1beta1.PodTemplate{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "sandbox",
								Image: "python:3.11-slim",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse(cpuQuantity),
										corev1.ResourceMemory: resource.MustParse(memQuantity),
									},
								},
							},
						},
						RestartPolicy: corev1.RestartPolicyAlways,
					},
				},
			},
		},
	}

	created, err := a.k8s.ExtensionsClient.SandboxTemplates(a.namespace).Create(ctx, tmpl, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("creating sandbox template: %w", err)
	}

	return &adapter.TemplateBuild{
		TemplateID: created.Name,
		BuildID:    buildID,
		Status:     "ready",
	}, nil
}

// DeleteTemplate removes a SandboxTemplate CRD by name.
func (a *Adapter) DeleteTemplate(ctx context.Context, templateID string) error {
	err := a.k8s.ExtensionsClient.SandboxTemplates(a.namespace).Delete(ctx, templateID, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("deleting sandbox template %q: %w", templateID, err)
	}
	return nil
}

// --- Template Builds ---

// TriggerBuild creates a new synthetic build for the template. Agent-sandbox
// templates are Kubernetes CRDs that become ready immediately when applied;
// there is no separate build process. A new build ID is generated and stored
// as an annotation on the CRD so GetBuildStatus can retrieve it.
func (a *Adapter) TriggerBuild(ctx context.Context, templateID string, req *adapter.BuildRequest) (*adapter.TemplateBuild, error) {
	// Verify the template exists.
	tmpl, err := a.k8s.ExtensionsClient.SandboxTemplates(a.namespace).Get(ctx, templateID, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting sandbox template %q: %w", templateID, err)
	}

	buildID := "build-" + generateE2BID()

	// Update annotations with the new build ID and optional Dockerfile/startCmd.
	if tmpl.Annotations == nil {
		tmpl.Annotations = make(map[string]string)
	}
	tmpl.Annotations[annotationBuildID] = buildID
	if req.Dockerfile != "" {
		tmpl.Annotations[annotationDockerfile] = req.Dockerfile
	}
	if req.StartCmd != "" {
		tmpl.Annotations[annotationStartCmd] = req.StartCmd
	}

	_, err = a.k8s.ExtensionsClient.SandboxTemplates(a.namespace).Update(ctx, tmpl, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("updating sandbox template build: %w", err)
	}

	return &adapter.TemplateBuild{
		TemplateID: templateID,
		BuildID:    buildID,
		Status:     "ready",
	}, nil
}

// GetBuildStatus returns the status of a template build. Since agent-sandbox
// templates are CRDs that are always ready, this always returns "ready".
func (a *Adapter) GetBuildStatus(ctx context.Context, templateID, buildID string) (*adapter.BuildStatus, error) {
	tmpl, err := a.k8s.ExtensionsClient.SandboxTemplates(a.namespace).Get(ctx, templateID, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting sandbox template %q: %w", templateID, err)
	}

	currentBuildID := ""
	if tmpl.Annotations != nil {
		currentBuildID = tmpl.Annotations[annotationBuildID]
	}
	// If the requested buildID matches the current one (or is empty), return ready.
	if buildID != "" && currentBuildID != "" && buildID != currentBuildID {
		// The requested build is not the current one; still return "ready"
		// since all builds on CRDs complete synchronously.
		return &adapter.BuildStatus{
			BuildID: buildID,
			Status:  "ready",
		}, nil
	}

	return &adapter.BuildStatus{
		BuildID: currentBuildID,
		Status:  "ready",
	}, nil
}

// --- Template Aliases ---

// CreateAlias adds an alias to a SandboxTemplate. Aliases are stored as a
// comma-separated list in the annotationAliases annotation on the CRD.
func (a *Adapter) CreateAlias(ctx context.Context, templateID string, alias string) error {
	if alias == "" {
		return fmt.Errorf("alias is required")
	}

	tmpl, err := a.k8s.ExtensionsClient.SandboxTemplates(a.namespace).Get(ctx, templateID, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting sandbox template %q: %w", templateID, err)
	}

	if tmpl.Annotations == nil {
		tmpl.Annotations = make(map[string]string)
	}

	existing := tmpl.Annotations[annotationAliases]
	// Check for duplicates.
	for existingAlias := range strings.SplitSeq(existing, ",") {
		if strings.TrimSpace(existingAlias) == alias {
			return nil // already exists
		}
	}

	if existing != "" {
		tmpl.Annotations[annotationAliases] = existing + "," + alias
	} else {
		tmpl.Annotations[annotationAliases] = alias
	}

	_, err = a.k8s.ExtensionsClient.SandboxTemplates(a.namespace).Update(ctx, tmpl, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating sandbox template aliases: %w", err)
	}
	return nil
}

// DeleteAlias removes an alias from a SandboxTemplate.
func (a *Adapter) DeleteAlias(ctx context.Context, templateID, alias string) error {
	tmpl, err := a.k8s.ExtensionsClient.SandboxTemplates(a.namespace).Get(ctx, templateID, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting sandbox template %q: %w", templateID, err)
	}

	if tmpl.Annotations == nil {
		return fmt.Errorf("alias %q not found for template %q", alias, templateID)
	}

	existing := tmpl.Annotations[annotationAliases]
	parts := strings.Split(existing, ",")
	found := false
	filtered := make([]string, 0, len(parts))
	for _, a := range parts {
		a = strings.TrimSpace(a)
		if a == alias {
			found = true
			continue
		}
		if a != "" {
			filtered = append(filtered, a)
		}
	}
	if !found {
		return fmt.Errorf("alias %q not found for template %q", alias, templateID)
	}

	tmpl.Annotations[annotationAliases] = strings.Join(filtered, ",")
	_, err = a.k8s.ExtensionsClient.SandboxTemplates(a.namespace).Update(ctx, tmpl, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating sandbox template aliases: %w", err)
	}
	return nil
}

// generateTemplateID produces a DNS-1123-safe template ID from a user-supplied name.
func generateTemplateID(name string) string {
	// Lowercase, replace non-alphanumeric with hyphens, truncate.
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return '-'
	}, name)
	sanitized = strings.Trim(sanitized, "-")
	if sanitized == "" {
		sanitized = "template"
	}
	// Append a short random suffix for uniqueness.
	return sanitized + "-" + generateE2BID()
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
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return err
	}
	// processID should be a real PID (from ListProcesses)
	// Validate it's a pure number to prevent shell injection
	var pid int
	var extra string
	n, err := fmt.Sscanf(processID, "%d%s", &pid, &extra)
	if n != 1 || (err != nil && err != io.EOF) {
		return fmt.Errorf("invalid process ID %q: must be a numeric PID", processID)
	}
	_, err = handle.Run(ctx, fmt.Sprintf("kill -9 %d", pid))
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

// --- Ports ---

// ListPorts returns the list of tracked ports for a sandbox.
// agent-sandbox does not provide a native API to list all open ports,
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
// It constructs the URL using the Pod's IP address and the specified port.
// The port is tracked for subsequent ListPorts calls.
func (a *Adapter) GetPortURL(ctx context.Context, sandboxID string, port int) (string, error) {
	podName, err := a.resolveSandboxCRName(ctx, sandboxID)
	if err != nil {
		return "", fmt.Errorf("resolving sandbox pod for port %d (sandbox %q): %w", port, sandboxID, err)
	}

	pod, err := a.k8s.CoreClient.Pods(a.namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting pod %q for port %d: %w", podName, port, err)
	}

	if pod.Status.PodIP == "" {
		return "", fmt.Errorf("pod %q has no IP yet (phase=%s)", podName, pod.Status.Phase)
	}

	url := fmt.Sprintf("http://%s:%d", pod.Status.PodIP, port)

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
// If a valid token already exists in cache, it is returned.
// Otherwise, a new token is generated and cached with 1h TTL.
func (a *Adapter) GetAccessToken(_ context.Context, sandboxID string) (*adapter.AccessToken, error) {
	// Check cache for existing token.
	if cached, ok := a.tokenCache.Get(sandboxID); ok {
		if tokenStr, ok := cached.(string); ok {
			return &adapter.AccessToken{
				Token:     tokenStr,
				ExpiresAt: time.Now().Add(1 * time.Hour),
			}, nil
		}
	}

	// Generate new token: envd_{sandboxID}_{32-hex-random}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generating token: %w", err)
	}
	token := fmt.Sprintf("envd_%s_%s", sandboxID, hex.EncodeToString(b))

	// Store in cache with 1h TTL.
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

// SetEnvs writes environment variables into the sandbox. It is part of the
// envd data plane like the other data-plane operations: when useEnvdDataPlane
// is true it executes via the envd ConnectRPC client, otherwise it falls back
// to the agent-sandbox SDK runtime handle.
func (a *Adapter) SetEnvs(ctx context.Context, sandboxID string, envs map[string]string) error {
	if a.useEnvdDataPlane {
		return a.setEnvsViaEnvd(ctx, sandboxID, envs)
	}
	return a.setEnvsViaHandle(ctx, sandboxID, envs)
}

// setEnvsViaEnvd appends the variables to /etc/environment using envd ConnectRPC.
// Each envd command spawns a new process, so export does not persist between
// commands — only the /etc/environment write is needed (matching the OpenSandbox
// SetEnvs behavior).
func (a *Adapter) setEnvsViaEnvd(ctx context.Context, sandboxID string, envs map[string]string) error {
	envdClient, err := a.getOrCreateEnvdClient(ctx, sandboxID)
	if err != nil {
		return err
	}

	cmd := buildEtcEnvironmentCmd(envs)
	if _, _, _, err := envdClient.RunCommand(ctx, cmd, "", nil); err != nil {
		return fmt.Errorf("writing to /etc/environment: %w", err)
	}
	return nil
}

// setEnvsViaHandle writes environment variables using the agent-sandbox SDK
// runtime handle (legacy path).
func (a *Adapter) setEnvsViaHandle(ctx context.Context, sandboxID string, envs map[string]string) error {
	handle, err := a.getHandle(ctx, sandboxID)
	if err != nil {
		return err
	}

	// Write environment variables to /etc/environment for persistence
	// Each Run() creates a new shell, so export doesn't persist
	cmd := buildEtcEnvironmentCmd(envs)
	if _, err := handle.Run(ctx, cmd); err != nil {
		return fmt.Errorf("writing to /etc/environment: %w", err)
	}

	// Also export in current shell for immediate use
	for k, v := range envs {
		if _, err := handle.Run(ctx, fmt.Sprintf("export %s=%s", k, util.ShellQuote(v))); err != nil {
			return err
		}
	}

	return nil
}

// buildEtcEnvironmentCmd builds the shell command that appends environment
// variables to /etc/environment. Each line is formatted KEY="value" (quoted to
// handle spaces/special chars) and the whole content is shell-quoted.
func buildEtcEnvironmentCmd(envs map[string]string) string {
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
	if a.useEnvdDataPlane {
		return a.moveFileViaEnvd(ctx, sandboxID, src, dst)
	}
	return a.moveFileViaHandle(ctx, sandboxID, src, dst)
}

// moveFileViaEnvd moves a file using envd ConnectRPC.
func (a *Adapter) moveFileViaEnvd(ctx context.Context, sandboxID string, src, dst string) error {
	envdClient, err := a.getOrCreateEnvdClient(ctx, sandboxID)
	if err != nil {
		return err
	}
	return envdClient.Move(ctx, src, dst)
}

// moveFileViaHandle moves a file using agent-sandbox SDK handle (legacy).
func (a *Adapter) moveFileViaHandle(ctx context.Context, sandboxID string, src, dst string) error {
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

	// Get access token from cache
	token := ""
	if tok, err := a.GetAccessToken(ctx, sandboxID); err == nil && tok != nil {
		token = tok.Token
	}

	return fmt.Sprintf("http://%s:49983", pod.Status.PodIP), token, nil
}

// generateE2BID generates an E2B-compatible sandbox ID (12 hex chars).
func generateE2BID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
