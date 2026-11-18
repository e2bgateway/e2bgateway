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

// Package agentsubstrate implements a SandboxAdapter backed by Agent
// Substrate environments (https://github.com/agent-substrate/env).
//
// Each sandbox is a Substrate actor managed through the ate-env-api gRPC
// service. Lifecycle calls go to EnvironmentService; command execution and
// file transfer are forwarded by ate-env-api to ate-env-guest inside the
// actor (ProcessService / FileSystemService), addressed with the x-env-id and
// x-env-atespace gRPC metadata headers.
package agentsubstrate

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/adapter/agentsubstrate/ateenvpb"
	"github.com/e2bgateway/e2bgateway/internal/adapter/util"
	"github.com/e2bgateway/e2bgateway/internal/sessionstore"
)

const (
	// BackendType is the backend type name used in gateway configuration.
	BackendType = "agent-substrate"

	// DefaultAtespace and DefaultActorTemplate match the ate-env defaults.
	DefaultAtespace      = "ate-env"
	DefaultActorTemplate = "default-template"

	defaultTemplateAlias = "base"
	defaultFileMode      = 0o644
	fileChunkSize        = 64 * 1024
	maxCommandOutput     = 4 << 20
	maxBufferedFileSize  = 16 << 20
	tokenTTL             = time.Hour
	// envsTTL is a sliding TTL (refreshed on read/write): Substrate enforces
	// no sandbox lifetime, so envs live as long as the sandbox keeps serving
	// traffic, matching the routing-entry convention.
	envsTTL = 24 * time.Hour
)

// Config configures the Agent Substrate adapter.
type Config struct {
	// Name is the adapter name reported to the registry.
	Name string
	// Endpoint is the host:port of the ate-env-api gRPC service. Required
	// unless Conn is set.
	Endpoint string
	// TLS enables TLS (system roots) for the gRPC connection.
	TLS bool
	// Conn overrides the connection created from Endpoint (tests).
	Conn *grpc.ClientConn
	// Atespace is the Substrate atespace environments live in.
	Atespace string
	// DefaultTemplate is the ActorTemplate used when the requested template
	// is empty or "base".
	DefaultTemplate string
	// TemplateToActor maps E2B template IDs to ActorTemplate names. Unmapped
	// template IDs are used as ActorTemplate names verbatim.
	TemplateToActor map[string]string
	// Registry records sandbox→backend ownership.
	Registry *adapter.Registry
}

type sandboxState struct {
	sandbox *adapter.Sandbox
}

// Adapter is the Agent Substrate SandboxAdapter.
type Adapter struct {
	cfg        Config
	conn       *grpc.ClientConn
	ownsConn   bool
	env        ateenvpb.EnvironmentServiceClient
	process    ateenvpb.ProcessServiceClient
	filesystem ateenvpb.FileSystemServiceClient

	mu           sync.RWMutex
	sandboxes    map[string]*sandboxState // replica-local index; Substrate has no list API
	sessionStore sessionstore.Store
}

var _ adapter.SandboxAdapter = (*Adapter)(nil)
var _ sessionstore.Aware = (*Adapter)(nil)

// New creates an Agent Substrate adapter.
func New(cfg Config) (*Adapter, error) {
	if cfg.Name == "" {
		cfg.Name = BackendType
	}
	if cfg.Atespace == "" {
		cfg.Atespace = DefaultAtespace
	}
	if cfg.DefaultTemplate == "" {
		cfg.DefaultTemplate = DefaultActorTemplate
	}

	a := &Adapter{
		cfg:          cfg,
		conn:         cfg.Conn,
		sandboxes:    make(map[string]*sandboxState),
		sessionStore: sessionstore.NewMemory(),
	}
	if a.conn == nil {
		if cfg.Endpoint == "" {
			return nil, errors.New("endpoint is required for the agent-substrate adapter")
		}
		conn, err := dial(cfg)
		if err != nil {
			return nil, err
		}
		a.conn = conn
		a.ownsConn = true
	}
	a.env = ateenvpb.NewEnvironmentServiceClient(a.conn)
	a.process = ateenvpb.NewProcessServiceClient(a.conn)
	a.filesystem = ateenvpb.NewFileSystemServiceClient(a.conn)
	return a, nil
}

// Close releases the gRPC connection if the adapter created it.
func (a *Adapter) Close() error {
	if a.ownsConn {
		return a.conn.Close()
	}
	return nil
}

func (a *Adapter) Name() string { return a.cfg.Name }

func unsupported(op string) error {
	return fmt.Errorf("%s is not supported by the agent-substrate backend: %w", op, adapter.ErrUnsupported)
}

// withEnv routes a guest RPC to the environment through ate-env-api.
func (a *Adapter) withEnv(ctx context.Context, sandboxID string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "x-env-id", sandboxID, "x-env-atespace", a.cfg.Atespace)
}

func mapError(sandboxID string, err error) error {
	if err == nil {
		return nil
	}
	if status.Code(err) == codes.NotFound {
		return fmt.Errorf("sandbox %q not found: %w", sandboxID, err)
	}
	return err
}

// HealthCheck verifies ate-env-api answers. A lookup of a nonexistent
// environment returning NotFound proves the service is reachable.
func (a *Adapter) HealthCheck(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := a.env.GetEnvironment(ctx, &ateenvpb.GetEnvironmentRequest{
		Id: "e2bgw-healthcheck", Atespace: a.cfg.Atespace,
	})
	if err != nil && status.Code(err) != codes.NotFound {
		return fmt.Errorf("ate-env-api health check: %w", err)
	}
	return nil
}

// --- Sandbox Lifecycle ---

func (a *Adapter) actorTemplate(templateID string) string {
	if m, ok := a.lookupTemplate(templateID); ok {
		return m
	}
	if templateID == "" || templateID == defaultTemplateAlias {
		return a.cfg.DefaultTemplate
	}
	return templateID
}

func (a *Adapter) lookupTemplate(templateID string) (string, bool) {
	if v, ok := a.cfg.TemplateToActor[templateID]; ok {
		return v, true
	}
	// Viper lowercases map keys.
	v, ok := a.cfg.TemplateToActor[strings.ToLower(templateID)]
	return v, ok
}

func (a *Adapter) CreateSandbox(ctx context.Context, req *adapter.CreateSandboxRequest) (*adapter.Sandbox, error) {
	id, err := util.GenerateE2BID()
	if err != nil {
		return nil, err
	}
	// Substrate environment IDs are DNS-1123 labels; a letter prefix keeps
	// the ID valid regardless of the random hex.
	id = "sb-" + id

	resp, err := a.env.CreateEnvironment(ctx, &ateenvpb.CreateEnvironmentRequest{
		Id:       id,
		Atespace: a.cfg.Atespace,
		Template: &ateenvpb.Template{Name: a.actorTemplate(req.TemplateID), Atespace: a.cfg.Atespace},
	})
	if err != nil {
		// The create may still have reached the server when the call fails
		// this way; best-effort delete so the environment cannot leak with no
		// gateway-visible reference.
		switch status.Code(err) {
		case codes.Canceled, codes.DeadlineExceeded, codes.Unavailable:
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if _, delErr := a.env.DeleteEnvironment(cleanupCtx, &ateenvpb.DeleteEnvironmentRequest{Id: id, Atespace: a.cfg.Atespace}); delErr != nil {
				// A racing in-flight create can outlive this delete, and no
				// reference to id survives: log loudly so an operator can
				// delete the orphan by ID.
				slog.Warn("failed to roll back environment; orphan possible", "sandboxID", id, "backend", a.cfg.Name, "error", delErr)
			}
		}
		return nil, fmt.Errorf("creating environment: %w", err)
	}

	now := time.Now()
	sbx := adapter.Sandbox{
		SandboxID:  id,
		TemplateID: req.TemplateID,
		Alias:      req.Alias,
		StartedAt:  now,
		Status:     convertStatus(resp.GetEnvironment().GetStatus()),
		Metadata:   maps.Clone(req.Metadata),
		Backend:    a.cfg.Name,
	}
	if a.cfg.Registry != nil {
		if err := a.cfg.Registry.SandboxBackend().Set(ctx, id, a.cfg.Name); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			// Roll back the environment directly (KillSandbox's early return
			// would skip the route delete, and the routing entry has not been
			// written yet, so there is nothing else to clean up).
			if _, delErr := a.env.DeleteEnvironment(cleanupCtx, &ateenvpb.DeleteEnvironmentRequest{Id: id, Atespace: a.cfg.Atespace}); delErr != nil {
				slog.Warn("failed to roll back sandbox; orphan possible", "sandboxID", id, "backend", a.cfg.Name, "error", delErr)
			}
			return nil, fmt.Errorf("recording sandbox routing: %w", err)
		}
	}

	if err := sessionstore.MergeEnvs(ctx, a.sessionStore, id, req.Envs, envsTTL); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if killErr := a.KillSandbox(cleanupCtx, id); killErr != nil {
			slog.Warn("failed to roll back sandbox", "sandboxID", id, "backend", a.cfg.Name, "error", killErr)
		}
		return nil, fmt.Errorf("storing sandbox envs: %w", err)
	}

	a.mu.Lock()
	a.sandboxes[id] = &sandboxState{sandbox: &sbx}
	out := sbx
	a.mu.Unlock()

	return &out, nil
}

// ListSandboxes returns sandboxes created through this gateway replica;
// the ate-env API has no list operation yet.
func (a *Adapter) ListSandboxes(ctx context.Context, opts adapter.ListOptions) ([]*adapter.Sandbox, error) {
	a.mu.RLock()
	candidates := make([]*adapter.Sandbox, 0, len(a.sandboxes))
	for _, st := range a.sandboxes {
		if st.sandbox == nil || !matchesLabels(st.sandbox.Metadata, opts.Labels) {
			continue
		}
		s := *st.sandbox
		candidates = append(candidates, &s)
	}
	a.mu.RUnlock()
	// The index is replica-local, so a kill done through another replica
	// would linger here. Confirm liveness against the backend and evict
	// entries that are definitely gone (transient errors keep the entry).
	result := candidates[:0]
	for _, s := range candidates {
		_, err := a.env.GetEnvironment(ctx, &ateenvpb.GetEnvironmentRequest{Id: s.SandboxID, Atespace: a.cfg.Atespace})
		if status.Code(err) == codes.NotFound {
			a.mu.Lock()
			if st, ok := a.sandboxes[s.SandboxID]; ok && st.sandbox != nil && st.sandbox.SandboxID == s.SandboxID {
				delete(a.sandboxes, s.SandboxID)
			}
			a.mu.Unlock()
			continue
		}
		if err != nil {
			slog.Warn("liveness check failed; keeping sandbox listing", "sandboxID", s.SandboxID, "backend", a.cfg.Name, "error", err)
		}
		result = append(result, s)
	}
	slices.SortFunc(result, func(left, right *adapter.Sandbox) int {
		return cmp.Compare(left.SandboxID, right.SandboxID)
	})
	if opts.Offset > 0 {
		if opts.Offset >= len(result) {
			return []*adapter.Sandbox{}, nil
		}
		result = result[opts.Offset:]
	}
	if opts.Limit > 0 && opts.Limit < len(result) {
		result = result[:opts.Limit]
	}
	return result, nil
}

func (a *Adapter) GetSandbox(ctx context.Context, sandboxID string) (*adapter.Sandbox, error) {
	resp, err := a.env.GetEnvironment(ctx, &ateenvpb.GetEnvironmentRequest{
		Id: sandboxID, Atespace: a.cfg.Atespace,
	})
	if err != nil {
		return nil, mapError(sandboxID, err)
	}
	st := convertStatus(resp.GetEnvironment().GetStatus())

	a.mu.Lock()
	defer a.mu.Unlock()
	if local, ok := a.sandboxes[sandboxID]; ok && local.sandbox != nil {
		local.sandbox.Status = st
		s := *local.sandbox
		return &s, nil
	}
	return &adapter.Sandbox{
		SandboxID:  sandboxID,
		TemplateID: resp.GetEnvironment().GetTemplate().GetName(),
		Status:     st,
		Backend:    a.cfg.Name,
	}, nil
}

func (a *Adapter) KillSandbox(ctx context.Context, sandboxID string) error {
	_, delErr := a.env.DeleteEnvironment(ctx, &ateenvpb.DeleteEnvironmentRequest{
		Id: sandboxID, Atespace: a.cfg.Atespace,
	})
	if delErr != nil && status.Code(delErr) != codes.NotFound {
		return mapError(sandboxID, delErr)
	}
	// A missing environment still needs cleanup: it may have been deleted
	// externally, reaped, or killed through another replica, and the ghost
	// entries would otherwise linger in ListSandboxes and the session store.
	// Use a bounded context so a client disconnect after the upstream delete
	// cannot silently skip the shared-state cleanup.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	a.mu.Lock()
	delete(a.sandboxes, sandboxID)
	a.mu.Unlock()
	for _, key := range []string{sessionstore.TokenKey(sandboxID), sessionstore.EnvsKey(sandboxID)} {
		if err := a.sessionStore.Delete(cleanupCtx, key); err != nil {
			slog.Warn("failed to delete sandbox session state; rerun the kill once the session store recovers", "sandboxID", sandboxID, "backend", a.cfg.Name, "error", err)
		}
	}
	if a.cfg.Registry != nil {
		if err := a.cfg.Registry.SandboxBackend().Delete(cleanupCtx, sandboxID); err != nil {
			slog.Warn("failed to delete sandbox routing entry; rerun the kill once the session store recovers", "sandboxID", sandboxID, "backend", a.cfg.Name, "error", err)
		}
	}
	if delErr != nil {
		return mapError(sandboxID, delErr)
	}
	return nil
}

func (a *Adapter) PauseSandbox(context.Context, string) error {
	return unsupported("pause")
}

func (a *Adapter) ResumeSandbox(context.Context, string) (*adapter.Sandbox, error) {
	return nil, unsupported("resume")
}

func (a *Adapter) SetTimeout(context.Context, string, time.Duration) error {
	return unsupported("set timeout")
}

// --- Code Execution ---

func (a *Adapter) ExecuteCode(ctx context.Context, sandboxID string, req *adapter.CodeExecutionRequest) (*adapter.CodeExecutionResult, error) {
	var command []string
	switch strings.ToLower(req.Language) {
	case "", "python", "python3":
		command = []string{"python3", "-c", req.Code}
	case "javascript", "js", "node":
		command = []string{"node", "-e", req.Code}
	case "bash", "sh", "shell":
		command = []string{"sh", "-c", req.Code}
	default:
		return nil, fmt.Errorf("language %q is not supported by the agent-substrate backend: %w", req.Language, adapter.ErrUnsupported)
	}

	start := time.Now()
	res, err := a.run(ctx, sandboxID, command, req.Cwd, req.EnvVars)
	if err != nil {
		return nil, err
	}
	out := &adapter.CodeExecutionResult{
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
		ExitCode: res.ExitCode,
		Duration: time.Since(start),
	}
	if res.ExitCode != 0 {
		out.Error = strings.TrimSpace(res.Stderr)
	}
	return out, nil
}

func (a *Adapter) ExecuteCodeStream(context.Context, string, *adapter.CodeExecutionRequest, adapter.CodeStream) error {
	return unsupported("streaming code execution")
}

func (a *Adapter) RunCommand(ctx context.Context, sandboxID string, req *adapter.CommandRequest) (*adapter.CommandResult, error) {
	command := req.Command
	if len(req.Args) > 0 {
		quoted := make([]string, len(req.Args))
		for i, arg := range req.Args {
			quoted[i] = util.ShellQuote(arg)
		}
		command += " " + strings.Join(quoted, " ")
	}
	return a.run(ctx, sandboxID, []string{"sh", "-c", command}, req.Cwd, req.EnvVars)
}

// run starts a process in the guest and buffers its output until exit.
func (a *Adapter) run(ctx context.Context, sandboxID string, command []string, cwd string, envs map[string]string) (*adapter.CommandResult, error) {
	ctx, cancel := context.WithCancel(a.withEnv(ctx, sandboxID))
	defer cancel()

	merged, err := a.sandboxEnvs(ctx, sandboxID)
	if err != nil {
		return nil, fmt.Errorf("loading envs for sandbox %q: %w", sandboxID, err)
	}
	maps.Copy(merged, envs)

	proc, err := a.process.StartProcess(ctx, &ateenvpb.StartProcessRequest{
		Command: command,
		Cwd:     cwd,
		Env:     merged,
	})
	if err != nil {
		return nil, mapError(sandboxID, err)
	}

	stream, err := a.process.StreamProcessOutput(ctx, &ateenvpb.StreamProcessOutputRequest{
		ProcessId: proc.GetProcessId(),
		Follow:    true,
	})
	if err != nil {
		return nil, mapError(sandboxID, err)
	}

	var stdout, stderr bytes.Buffer
	var exit *ateenvpb.Process
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, mapError(sandboxID, err)
		}
		var output []byte
		var dst *bytes.Buffer
		switch out := msg.GetOutput().(type) {
		case *ateenvpb.ProcessOutput_Stdout:
			output, dst = out.Stdout, &stdout
		case *ateenvpb.ProcessOutput_Stderr:
			output, dst = out.Stderr, &stderr
		case *ateenvpb.ProcessOutput_Exit:
			exit = out.Exit
			continue
		default:
			continue
		}
		if len(output) > maxCommandOutput-stdout.Len()-stderr.Len() {
			return nil, status.Errorf(codes.ResourceExhausted, "command output exceeds %d bytes", maxCommandOutput)
		}
		dst.Write(output)
	}
	if exit == nil {
		return nil, errors.New("process output stream ended before the process exited")
	}
	return &adapter.CommandResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: int(exit.GetExitCode()),
	}, nil
}

// runChecked runs a command and converts a nonzero exit into an error.
func (a *Adapter) runChecked(ctx context.Context, sandboxID string, command ...string) (*adapter.CommandResult, error) {
	res, err := a.run(ctx, sandboxID, command, "", nil)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("%s failed (exit %d): %s", command[0], res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return res, nil
}

// --- Filesystem ---

func (a *Adapter) WriteFile(ctx context.Context, sandboxID string, req *adapter.FileWriteRequest) error {
	return a.writeStream(ctx, sandboxID, req.Path, bytes.NewReader(req.Content))
}

func (a *Adapter) UploadFile(ctx context.Context, sandboxID string, req *adapter.FileUploadRequest) error {
	if req.Reader == nil {
		return errors.New("upload reader is required")
	}
	defer req.Reader.Close()
	return a.writeStream(ctx, sandboxID, req.Path, req.Reader)
}

func (a *Adapter) writeStream(ctx context.Context, sandboxID, path string, r io.Reader) error {
	if path == "" {
		return errors.New("file path is required")
	}
	ctx, cancel := context.WithCancel(a.withEnv(ctx, sandboxID))
	defer cancel()
	stream, err := a.filesystem.WriteFile(ctx)
	if err != nil {
		return mapError(sandboxID, err)
	}

	buf := make([]byte, fileChunkSize)
	first := true
	for {
		n, readErr := r.Read(buf)
		if n > 0 || first {
			msg := &ateenvpb.WriteFileRequest{Chunk: append([]byte(nil), buf[:n]...)}
			if first {
				msg.Path = path
				msg.Mode = defaultFileMode
				first = false
			}
			if err := stream.Send(msg); err != nil {
				// Send only reports EOF; the real status comes from CloseAndRecv.
				if _, recvErr := stream.CloseAndRecv(); recvErr != nil {
					return fmt.Errorf("writing file %q in sandbox %q: %w", path, sandboxID, recvErr)
				}
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = stream.CloseSend()
			return fmt.Errorf("reading upload: %w", readErr)
		}
	}
	if _, err := stream.CloseAndRecv(); err != nil {
		return fmt.Errorf("writing file %q in sandbox %q: %w", path, sandboxID, err)
	}
	return nil
}

func (a *Adapter) ReadFile(ctx context.Context, sandboxID string, path string) (*adapter.FileContent, error) {
	reader, err := a.DownloadFile(ctx, sandboxID, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(io.LimitReader(reader, maxBufferedFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBufferedFileSize {
		return nil, status.Errorf(codes.ResourceExhausted, "file exceeds %d bytes; use the download endpoint", maxBufferedFileSize)
	}
	return &adapter.FileContent{Path: path, Content: data, Size: int64(len(data))}, nil
}

func (a *Adapter) DownloadFile(ctx context.Context, sandboxID string, path string) (io.ReadCloser, error) {
	if path == "" {
		return nil, errors.New("file path is required")
	}
	ctx, cancel := context.WithCancel(a.withEnv(ctx, sandboxID))
	stream, err := a.filesystem.ReadFile(ctx, &ateenvpb.ReadFileRequest{Path: path})
	if err != nil {
		cancel()
		return nil, mapError(sandboxID, err)
	}
	reader := &fileDownload{stream: stream, cancel: cancel, path: path}
	// Check the initial status before handing the reader to the HTTP handler.
	if err := reader.receiveChunk(); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return reader, nil
}

type fileDownload struct {
	stream grpc.ServerStreamingClient[ateenvpb.ReadFileResponse]
	cancel context.CancelFunc
	path   string
	chunk  []byte
	err    error
}

func (r *fileDownload) receiveChunk() error {
	for len(r.chunk) == 0 && r.err == nil {
		msg, err := r.stream.Recv()
		if err != nil {
			r.cancel()
			if status.Code(err) == codes.NotFound {
				err = fmt.Errorf("file %q not found: %w", r.path, err)
			} else if !errors.Is(err, io.EOF) {
				err = fmt.Errorf("reading file %q: %w", r.path, err)
			}
			r.err = err
			break
		}
		r.chunk = msg.GetChunk()
	}
	return r.err
}

func (r *fileDownload) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := r.receiveChunk(); err != nil {
		return 0, err
	}
	n := copy(p, r.chunk)
	r.chunk = r.chunk[n:]
	return n, nil
}

func (r *fileDownload) Close() error {
	r.cancel()
	return nil
}

// ListFiles lists a directory through guest exec (GNU find) because the
// guest API has no directory listing yet (agent-substrate/env#74).
func (a *Adapter) ListFiles(ctx context.Context, sandboxID string, path string) ([]adapter.FileInfo, error) {
	if path == "" {
		path = "/"
	}
	res, err := a.runChecked(ctx, sandboxID,
		"find", safeArg(path), "-mindepth", "1", "-maxdepth", "1",
		"-printf", `%y\t%s\t%T@\t%f\0`)
	if err != nil {
		return nil, err
	}
	return parseFindOutput(path, res.Stdout)
}

func parseFindOutput(dir, out string) ([]adapter.FileInfo, error) {
	// Find reports symlinks as links. Unparseable sizes are left at zero.
	var files []adapter.FileInfo
	for rec := range strings.SplitSeq(out, "\x00") {
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\t", 4)
		if len(parts) != 4 {
			return nil, fmt.Errorf("unexpected listing record %q", rec)
		}
		size, _ := strconv.ParseInt(parts[1], 10, 64)
		var mod time.Time
		if secs, err := strconv.ParseFloat(parts[2], 64); err == nil {
			mod = time.Unix(int64(secs), 0).UTC()
		}
		files = append(files, adapter.FileInfo{
			Name:    parts[3],
			Path:    strings.TrimRight(dir, "/") + "/" + parts[3],
			Size:    size,
			IsDir:   parts[0] == "d",
			ModTime: mod,
		})
	}
	return files, nil
}

func (a *Adapter) MakeDir(ctx context.Context, sandboxID string, path string) error {
	_, err := a.runChecked(ctx, sandboxID, "mkdir", "-p", "--", path)
	return err
}

func (a *Adapter) RemoveFile(ctx context.Context, sandboxID string, path string) error {
	_, err := a.runChecked(ctx, sandboxID, "rm", "-rf", "--", path)
	return err
}

func (a *Adapter) MoveFile(ctx context.Context, sandboxID string, src, dst string) error {
	_, err := a.runChecked(ctx, sandboxID, "mv", "--", src, dst)
	return err
}

// safeArg prevents a user path from being parsed as an option by find.
func safeArg(p string) string {
	if strings.HasPrefix(p, "-") {
		return "./" + p
	}
	return p
}

// --- Templates ---
//
// Substrate ActorTemplates are registered out-of-band with kubectl-ate; the
// gateway only maps E2B template IDs onto them.

func (a *Adapter) ListTemplates(_ context.Context, _ adapter.ListOptions) ([]*adapter.Template, error) {
	result := []*adapter.Template{a.template(defaultTemplateAlias)}
	for id := range a.cfg.TemplateToActor {
		if id != defaultTemplateAlias {
			result = append(result, a.template(id))
		}
	}
	return result, nil
}

func (a *Adapter) GetTemplate(_ context.Context, templateID string) (*adapter.Template, error) {
	if templateID != defaultTemplateAlias {
		if _, ok := a.lookupTemplate(templateID); !ok {
			return nil, fmt.Errorf("template %q not found", templateID)
		}
	}
	return a.template(templateID), nil
}

func (a *Adapter) template(id string) *adapter.Template {
	return &adapter.Template{
		TemplateID:  id,
		Name:        id,
		Description: "Agent Substrate ActorTemplate " + a.actorTemplate(id),
		Public:      true,
	}
}

func (a *Adapter) CreateTemplate(context.Context, *adapter.CreateTemplateRequest) (*adapter.TemplateBuild, error) {
	return nil, unsupported("template creation")
}

func (a *Adapter) DeleteTemplate(context.Context, string) error {
	return unsupported("template deletion")
}

func (a *Adapter) TriggerBuild(context.Context, string, *adapter.BuildRequest) (*adapter.TemplateBuild, error) {
	return nil, unsupported("template builds")
}

func (a *Adapter) GetBuildStatus(context.Context, string, string) (*adapter.BuildStatus, error) {
	return nil, unsupported("template builds")
}

func (a *Adapter) CreateAlias(context.Context, string, string) error {
	return unsupported("template aliases")
}

func (a *Adapter) DeleteAlias(context.Context, string, string) error {
	return unsupported("template aliases")
}

func (a *Adapter) CreateTag(context.Context, string, *adapter.TagRequest) (*adapter.Tag, error) {
	return nil, unsupported("template tags")
}

func (a *Adapter) ListTags(context.Context, string) ([]*adapter.Tag, error) {
	return nil, unsupported("template tags")
}

func (a *Adapter) DeleteTag(context.Context, string, string) error {
	return unsupported("template tags")
}

// --- Warm Pools ---

func (a *Adapter) ListWarmPools(context.Context) ([]*adapter.WarmPool, error) {
	return []*adapter.WarmPool{}, nil
}

func (a *Adapter) CreateWarmPool(context.Context, *adapter.WarmPoolCreateRequest) (*adapter.WarmPool, error) {
	return nil, unsupported("warm pools")
}

func (a *Adapter) GetWarmPool(context.Context, string) (*adapter.WarmPool, error) {
	return nil, unsupported("warm pools")
}

func (a *Adapter) DeleteWarmPool(context.Context, string, *adapter.WarmPoolDeleteOptions) error {
	return unsupported("warm pools")
}

func (a *Adapter) UpdateWarmPoolSize(context.Context, string, int) error {
	return unsupported("warm pools")
}

// --- Processes, Snapshots, Ports, Logs ---

func (a *Adapter) ListProcesses(context.Context, string) ([]*adapter.ProcessInfo, error) {
	return nil, unsupported("process listing")
}

func (a *Adapter) KillProcess(context.Context, string, string) error {
	return unsupported("process kill")
}

func (a *Adapter) SendStdin(context.Context, string, string, string) error {
	return unsupported("stdin")
}

func (a *Adapter) CreateSnapshot(context.Context, string, *adapter.SnapshotRequest) (*adapter.Snapshot, error) {
	return nil, unsupported("snapshots")
}

func (a *Adapter) ListSnapshots(context.Context, string) ([]*adapter.Snapshot, error) {
	return nil, unsupported("snapshots")
}

func (a *Adapter) ListPorts(context.Context, string) ([]*adapter.PortInfo, error) {
	return nil, unsupported("port listing")
}

func (a *Adapter) GetPortURL(context.Context, string, int) (string, error) {
	return "", unsupported("port forwarding")
}

func (a *Adapter) GetLogs(context.Context, string) ([]*adapter.LogEntry, error) {
	return nil, unsupported("logs")
}

// --- Environment Variables ---

// SetEnvs merges variables into the sandbox's stored set (shared across
// replicas through the session store) and injects them into every subsequent
// process started through the adapter.
func (a *Adapter) SetEnvs(ctx context.Context, sandboxID string, envs map[string]string) error {
	if _, err := a.GetSandbox(ctx, sandboxID); err != nil {
		return err
	}
	if err := sessionstore.MergeEnvs(ctx, a.sessionStore, sandboxID, envs, envsTTL); err != nil {
		return fmt.Errorf("storing envs for sandbox %q: %w", sandboxID, err)
	}
	return nil
}

func (a *Adapter) sandboxEnvs(ctx context.Context, sandboxID string) (map[string]string, error) {
	return sessionstore.GetEnvs(ctx, a.sessionStore, sandboxID, envsTTL)
}

// --- Access Token ---

// SetSessionStore injects the gateway's configured session store.
func (a *Adapter) SetSessionStore(store sessionstore.Store) {
	a.sessionStore = store
}

func (a *Adapter) GetAccessToken(ctx context.Context, sandboxID string) (*adapter.AccessToken, error) {
	if _, err := a.GetSandbox(ctx, sandboxID); err != nil {
		return nil, err
	}
	tok, expiry, err := sessionstore.MintToken(ctx, a.sessionStore, sandboxID, func() (string, time.Time, error) {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return "", time.Time{}, fmt.Errorf("generating access token: %w", err)
		}
		tok := fmt.Sprintf("envd_%s_%s", sandboxID, hex.EncodeToString(b))
		return tok, time.Now().Add(tokenTTL), nil
	})
	if err != nil {
		return nil, fmt.Errorf("storing access token: %w", err)
	}
	return &adapter.AccessToken{Token: tok, ExpiresAt: expiry}, nil
}

func (a *Adapter) ValidateAccessToken(ctx context.Context, sandboxID, token string) (bool, error) {
	tok, _, ok, err := sessionstore.GetToken(ctx, a.sessionStore, sandboxID)
	if err != nil || !ok {
		return false, err
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(token)) == 1, nil
}

// --- envd Data Plane ---

// GetEnvdEndpoint is unsupported: Substrate actors run ate-env-guest rather
// than envd, so SDK ConnectRPC traffic cannot be reverse-proxied yet. Use the
// gateway REST endpoints (commands, files) until an envd translation layer
// lands.
func (a *Adapter) GetEnvdEndpoint(context.Context, string) (string, string, error) {
	return "", "", unsupported("envd data plane")
}

// --- helpers ---

func convertStatus(s ateenvpb.EnvironmentStatus) adapter.SandboxStatus {
	switch s {
	case ateenvpb.EnvironmentStatus_ENVIRONMENT_STATUS_UNSPECIFIED,
		ateenvpb.EnvironmentStatus_ENVIRONMENT_STATUS_RESUMING:
	case ateenvpb.EnvironmentStatus_ENVIRONMENT_STATUS_RUNNING:
		return adapter.SandboxStatusRunning
	case ateenvpb.EnvironmentStatus_ENVIRONMENT_STATUS_SUSPENDING,
		ateenvpb.EnvironmentStatus_ENVIRONMENT_STATUS_SUSPENDED,
		ateenvpb.EnvironmentStatus_ENVIRONMENT_STATUS_PAUSING,
		ateenvpb.EnvironmentStatus_ENVIRONMENT_STATUS_PAUSED:
		return adapter.SandboxStatusPaused
	case ateenvpb.EnvironmentStatus_ENVIRONMENT_STATUS_CRASHED:
		return adapter.SandboxStatusError
	case ateenvpb.EnvironmentStatus_ENVIRONMENT_STATUS_DELETING:
		return adapter.SandboxStatusStopped
	}
	return adapter.SandboxStatusStarting
}

func matchesLabels(meta, want map[string]string) bool {
	for k, v := range want {
		if meta[k] != v {
			return false
		}
	}
	return true
}
