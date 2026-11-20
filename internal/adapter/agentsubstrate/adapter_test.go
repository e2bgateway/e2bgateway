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

package agentsubstrate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/adapter/agentsubstrate/ateenvpb"
	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/sessionstore"
)

type fakeServer struct {
	ateenvpb.UnimplementedEnvironmentServiceServer
	ateenvpb.UnimplementedProcessServiceServer
	ateenvpb.UnimplementedFileSystemServiceServer

	mu        sync.Mutex
	envs      map[string]*ateenvpb.Environment
	files     map[string][]byte
	started   []*ateenvpb.StartProcessRequest
	envMeta   []string
	stdout    string
	stderr    string
	exitCode  int32
	signals   []*ateenvpb.SignalProcessRequest
	lookups   int
	getHook   func() error
	readHook  func(*ateenvpb.ReadFileRequest, grpc.ServerStreamingServer[ateenvpb.ReadFileResponse]) error
	writeHook func(grpc.ClientStreamingServer[ateenvpb.WriteFileRequest, ateenvpb.WriteFileResponse]) error
}

func (f *fakeServer) CreateEnvironment(_ context.Context, r *ateenvpb.CreateEnvironmentRequest) (*ateenvpb.CreateEnvironmentResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	env := &ateenvpb.Environment{
		Id: r.Id, Atespace: r.Atespace, Template: r.Template,
		Status: ateenvpb.EnvironmentStatus_ENVIRONMENT_STATUS_RUNNING,
	}
	f.envs[r.Id] = env
	return &ateenvpb.CreateEnvironmentResponse{Environment: env}, nil
}

func (f *fakeServer) GetEnvironment(_ context.Context, r *ateenvpb.GetEnvironmentRequest) (*ateenvpb.GetEnvironmentResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	if f.getHook != nil {
		if err := f.getHook(); err != nil {
			return nil, err
		}
	}
	env, ok := f.envs[r.Id]
	if !ok {
		return nil, status.Error(codes.NotFound, "no such env")
	}
	return &ateenvpb.GetEnvironmentResponse{Environment: env}, nil
}

func (f *fakeServer) DeleteEnvironment(_ context.Context, r *ateenvpb.DeleteEnvironmentRequest) (*ateenvpb.DeleteEnvironmentResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.envs[r.Id]; !ok {
		return nil, status.Error(codes.NotFound, "no such env")
	}
	delete(f.envs, r.Id)
	return &ateenvpb.DeleteEnvironmentResponse{}, nil
}

func (f *fakeServer) StartProcess(ctx context.Context, r *ateenvpb.StartProcessRequest) (*ateenvpb.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, r)
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		f.envMeta = append(f.envMeta, md.Get("x-env-id")...)
	}
	return &ateenvpb.Process{ProcessId: "p1"}, nil
}

func (f *fakeServer) SignalProcess(_ context.Context, r *ateenvpb.SignalProcessRequest) (*ateenvpb.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signals = append(f.signals, r)
	return &ateenvpb.Process{ProcessId: r.ProcessId}, nil
}

func (f *fakeServer) StreamProcessOutput(_ *ateenvpb.StreamProcessOutputRequest, s grpc.ServerStreamingServer[ateenvpb.ProcessOutput]) error {
	f.mu.Lock()
	out, errOut, code := f.stdout, f.stderr, f.exitCode
	f.mu.Unlock()
	if out != "" {
		_ = s.Send(&ateenvpb.ProcessOutput{Output: &ateenvpb.ProcessOutput_Stdout{Stdout: []byte(out)}})
	}
	if errOut != "" {
		_ = s.Send(&ateenvpb.ProcessOutput{Output: &ateenvpb.ProcessOutput_Stderr{Stderr: []byte(errOut)}})
	}
	return s.Send(&ateenvpb.ProcessOutput{Output: &ateenvpb.ProcessOutput_Exit{
		Exit: &ateenvpb.Process{ProcessId: "p1", ExitCode: code, State: ateenvpb.ProcessState_PROCESS_STATE_EXITED},
	}})
}

func (f *fakeServer) WriteFile(s grpc.ClientStreamingServer[ateenvpb.WriteFileRequest, ateenvpb.WriteFileResponse]) error {
	if f.writeHook != nil {
		return f.writeHook(s)
	}
	var path string
	var buf bytes.Buffer
	for {
		m, err := s.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if m.Path != "" {
			path = m.Path
		}
		buf.Write(m.Chunk)
	}
	f.mu.Lock()
	f.files[path] = buf.Bytes()
	f.mu.Unlock()
	return s.SendAndClose(&ateenvpb.WriteFileResponse{BytesWritten: int64(buf.Len())})
}

func (f *fakeServer) ReadFile(r *ateenvpb.ReadFileRequest, s grpc.ServerStreamingServer[ateenvpb.ReadFileResponse]) error {
	if f.readHook != nil {
		return f.readHook(r, s)
	}
	f.mu.Lock()
	data, ok := f.files[r.Path]
	f.mu.Unlock()
	if !ok {
		return status.Error(codes.NotFound, "no such file")
	}
	for i := 0; i < len(data); i += 4 {
		end := min(i+4, len(data))
		if err := s.Send(&ateenvpb.ReadFileResponse{Chunk: data[i:end]}); err != nil {
			return err
		}
	}
	return nil
}

func newTestAdapter(t *testing.T, mutate func(*Config)) (*Adapter, *fakeServer) {
	t.Helper()
	fake := &fakeServer{envs: map[string]*ateenvpb.Environment{}, files: map[string][]byte{}}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	ateenvpb.RegisterEnvironmentServiceServer(srv, fake)
	ateenvpb.RegisterProcessServiceServer(srv, fake)
	ateenvpb.RegisterFileSystemServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	cfg := Config{Name: "substrate", Conn: conn, Registry: adapter.NewRegistry(sessionstore.NewMemory(), time.Hour)}
	if mutate != nil {
		mutate(&cfg)
	}
	a, err := New(cfg)
	require.NoError(t, err)
	return a, fake
}

func TestGetTemplate_UnknownWrapsSentinel(t *testing.T) {
	a, _ := newTestAdapter(t, func(c *Config) {
		c.TemplateToActor = map[string]string{"py": "python-actor"}
	})

	_, err := a.GetTemplate(context.Background(), "unmapped")
	require.Error(t, err)
	assert.True(t, errors.Is(err, adapter.ErrTemplateNotFound), "got %v, want ErrTemplateNotFound", err)

	// The wrapped sentinel must expose the 404 HTTP status contract so shared
	// handlers classify a clean miss as not-found instead of a backend failure.
	var statusErr interface{ HTTPStatusCode() int }
	require.True(t, errors.As(err, &statusErr))
	assert.Equal(t, 404, statusErr.HTTPStatusCode())

	// Mapped templates still resolve.
	tpl, err := a.GetTemplate(context.Background(), "py")
	require.NoError(t, err)
	assert.Equal(t, "py", tpl.TemplateID)
}

func TestSandboxLifecycle(t *testing.T) {
	a, fake := newTestAdapter(t, func(c *Config) {
		c.TemplateToActor = map[string]string{"py": "python-actor"}
	})
	ctx := context.Background()
	require.NoError(t, a.HealthCheck(ctx))

	sbx, err := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "py", Timeout: 60, Metadata: map[string]string{"k": "v"}})
	require.NoError(t, err)
	assert.Equal(t, adapter.SandboxStatusRunning, sbx.Status)
	assert.True(t, sbx.EndAt.IsZero())
	assert.Equal(t, "python-actor", fake.envs[sbx.SandboxID].Template.Name)
	assert.Equal(t, DefaultAtespace, fake.envs[sbx.SandboxID].Atespace)
	owner, found, err := a.cfg.Registry.SandboxBackend().Get(ctx, sbx.SandboxID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "substrate", owner)

	got, err := a.GetSandbox(ctx, sbx.SandboxID)
	require.NoError(t, err)
	assert.Equal(t, "v", got.Metadata["k"])

	list, err := a.ListSandboxes(ctx, adapter.ListOptions{Labels: map[string]string{"k": "v"}})
	require.NoError(t, err)
	assert.Len(t, list, 1)

	base, err := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "base"})
	require.NoError(t, err)
	assert.Equal(t, DefaultActorTemplate, fake.envs[base.SandboxID].Template.Name)

	// A kill through another replica leaves a ghost entry on this replica;
	// listing evicts it once the backend confirms the environment is gone.
	a.mu.Lock()
	a.sandboxes["sb-ghost"] = &sandboxState{sandbox: &adapter.Sandbox{SandboxID: "sb-ghost"}}
	a.mu.Unlock()
	_, err = a.ListSandboxes(ctx, adapter.ListOptions{})
	require.NoError(t, err)
	a.mu.RLock()
	assert.NotContains(t, a.sandboxes, "sb-ghost")
	a.mu.RUnlock()
	// A transient lookup error must not evict the entry.
	fake.getHook = func() error { return status.Error(codes.Unavailable, "backend down") }
	list, err = a.ListSandboxes(ctx, adapter.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, list, 2)
	fake.getHook = nil

	require.NoError(t, a.SetEnvs(ctx, sbx.SandboxID, map[string]string{"K": "V"}))

	require.NoError(t, a.KillSandbox(ctx, sbx.SandboxID))
	_, err = a.GetSandbox(ctx, sbx.SandboxID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	envs, err := sessionstore.GetEnvs(ctx, a.sessionStore, sbx.SandboxID, 0)
	require.NoError(t, err)
	assert.Empty(t, envs)

	// A repeated kill reports not found yet still cleans up any leftover
	// local and routing state.
	a.mu.Lock()
	a.sandboxes[sbx.SandboxID] = &sandboxState{sandbox: &adapter.Sandbox{SandboxID: sbx.SandboxID}}
	a.mu.Unlock()
	require.NoError(t, a.cfg.Registry.SandboxBackend().Set(ctx, sbx.SandboxID, "substrate"))
	assert.Error(t, a.KillSandbox(ctx, sbx.SandboxID))
	a.mu.RLock()
	assert.NotContains(t, a.sandboxes, sbx.SandboxID)
	a.mu.RUnlock()
	list, err = a.ListSandboxes(ctx, adapter.ListOptions{})
	require.NoError(t, err)
	for _, s := range list {
		assert.NotEqual(t, sbx.SandboxID, s.SandboxID)
	}
	_, found, err = a.cfg.Registry.SandboxBackend().Get(ctx, sbx.SandboxID)
	require.NoError(t, err)
	assert.False(t, found)
}

func TestSetEnvsSharedViaSessionStore(t *testing.T) {
	a, fake := newTestAdapter(t, nil)
	ctx := context.Background()
	fake.mu.Lock()
	fake.envs["external"] = &ateenvpb.Environment{Id: "external"}
	fake.mu.Unlock()

	shared := sessionstore.NewMemory()
	a.SetSessionStore(shared)
	// A second gateway replica sharing only the session store.
	b, err := New(Config{Name: "substrate", Conn: a.cfg.Conn})
	require.NoError(t, err)
	b.SetSessionStore(shared)

	require.NoError(t, a.SetEnvs(ctx, "external", map[string]string{"A": "1"}))
	// Sequential merges accumulate; concurrent cross-replica merges are
	// last-write-wins (see sessionstore.MergeEnvs).
	require.NoError(t, b.SetEnvs(ctx, "external", map[string]string{"B": "2"}))
	envs, err := b.sandboxEnvs(ctx, "external")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"A": "1", "B": "2"}, envs)
}

func TestListSandboxesPagination(t *testing.T) {
	a, fake := newTestAdapter(t, nil)
	ctx := t.Context()
	fake.mu.Lock()
	fake.envs["external"] = &ateenvpb.Environment{Id: "external"}
	fake.mu.Unlock()
	require.NoError(t, a.SetEnvs(ctx, "external", map[string]string{"A": "1"}))
	envs, err := a.sandboxEnvs(ctx, "external")
	require.NoError(t, err)
	assert.Equal(t, "1", envs["A"])

	for _, id := range []string{"sb-c", "sb-a", "sb-b"} {
		a.sandboxes[id] = &sandboxState{sandbox: &adapter.Sandbox{SandboxID: id}}
		fake.mu.Lock()
		fake.envs[id] = &ateenvpb.Environment{Id: id}
		fake.mu.Unlock()
	}
	for offset, id := range []string{"sb-a", "sb-b", "sb-c"} {
		page, err := a.ListSandboxes(ctx, adapter.ListOptions{Offset: offset, Limit: 1})
		require.NoError(t, err)
		require.Len(t, page, 1)
		assert.Equal(t, id, page[0].SandboxID)
	}
	page, err := a.ListSandboxes(ctx, adapter.ListOptions{Offset: 3, Limit: 1})
	require.NoError(t, err)
	assert.Empty(t, page)
}

func TestRunCommand(t *testing.T) {
	a, fake := newTestAdapter(t, nil)
	ctx := context.Background()
	sbx, err := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{Envs: map[string]string{"A": "1"}})
	require.NoError(t, err)
	fake.stdout, fake.stderr, fake.exitCode = "hello\n", "warn\n", 3

	res, err := a.RunCommand(ctx, sbx.SandboxID, &adapter.CommandRequest{
		Command: "echo", Args: []string{"a b", "$(rm -rf /)"}, Cwd: "/tmp", EnvVars: map[string]string{"B": "2"},
	})
	require.NoError(t, err)
	assert.Equal(t, "hello\n", res.Stdout)
	assert.Equal(t, "warn\n", res.Stderr)
	assert.Equal(t, 3, res.ExitCode)

	req := fake.started[0]
	assert.Equal(t, []string{"sh", "-c", `echo 'a b' '$(rm -rf /)'`}, req.Command)
	assert.Equal(t, "/tmp", req.Cwd)
	assert.Equal(t, map[string]string{"A": "1", "B": "2"}, req.Env)
	assert.Equal(t, []string{sbx.SandboxID}, fake.envMeta)

	require.NoError(t, a.SetEnvs(ctx, sbx.SandboxID, map[string]string{"C": "3"}))
	_, err = a.ExecuteCode(ctx, sbx.SandboxID, &adapter.CodeExecutionRequest{Code: "print(1)"})
	require.NoError(t, err)
	last := fake.started[1]
	assert.Equal(t, []string{"python3", "-c", "print(1)"}, last.Command)
	assert.Equal(t, "3", last.Env["C"])

	_, err = a.ExecuteCode(ctx, sbx.SandboxID, &adapter.CodeExecutionRequest{Code: "x", Language: "cobol"})
	assert.ErrorIs(t, err, adapter.ErrUnsupported)
}

func TestFileOperations(t *testing.T) {
	a, fake := newTestAdapter(t, nil)
	ctx := context.Background()
	sbx, err := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{})
	require.NoError(t, err)

	content := []byte("binary\x00\xffdata spanning several chunks")
	require.NoError(t, a.WriteFile(ctx, sbx.SandboxID, &adapter.FileWriteRequest{Path: "/a.bin", Content: content}))
	got, err := a.ReadFile(ctx, sbx.SandboxID, "/a.bin")
	require.NoError(t, err)
	assert.Equal(t, content, got.Content)
	assert.Equal(t, int64(len(content)), got.Size)

	reader, err := a.DownloadFile(ctx, sbx.SandboxID, "/a.bin")
	require.NoError(t, err)
	downloaded, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	assert.Equal(t, content, downloaded)

	require.NoError(t, a.UploadFile(ctx, sbx.SandboxID, &adapter.FileUploadRequest{
		Path: "/empty", Reader: io.NopCloser(bytes.NewReader(nil)),
	}))
	assert.Contains(t, fake.files, "/empty")
	reader, err = a.DownloadFile(ctx, sbx.SandboxID, "/empty")
	require.NoError(t, err)
	downloaded, err = io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	assert.Empty(t, downloaded)

	_, err = a.ReadFile(ctx, sbx.SandboxID, "/missing")
	assert.Equal(t, codes.NotFound, status.Code(err))

	fake.stdout = "d\t4096\t1700000000.5\tsub dir\x00f\t12\t1700000001.0\ta.txt\x00"
	files, err := a.ListFiles(ctx, sbx.SandboxID, "/work/")
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.Equal(t, "sub dir", files[0].Name)
	assert.Equal(t, "/work/sub dir", files[0].Path)
	assert.True(t, files[0].IsDir)
	assert.False(t, files[1].IsDir)
	assert.Equal(t, int64(12), files[1].Size)

	fake.stdout = ""
	_, err = a.ListFiles(ctx, sbx.SandboxID, "-rf")
	require.NoError(t, err)
	assert.Equal(t, "./-rf", fake.started[len(fake.started)-1].Command[1])

	require.NoError(t, a.MakeDir(ctx, sbx.SandboxID, "/x y"))
	assert.Equal(t, []string{"mkdir", "-p", "--", "/x y"}, fake.started[len(fake.started)-1].Command)

	fake.exitCode = 1
	fake.stderr = "denied"
	err = a.RemoveFile(ctx, sbx.SandboxID, "/etc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "denied")
}

func TestDownloadFile_CloseCancelsStream(t *testing.T) {
	a, fake := newTestAdapter(t, nil)
	canceled := make(chan struct{})
	fake.readHook = func(_ *ateenvpb.ReadFileRequest, stream grpc.ServerStreamingServer[ateenvpb.ReadFileResponse]) error {
		if err := stream.Send(&ateenvpb.ReadFileResponse{Chunk: []byte("data")}); err != nil {
			return err
		}
		<-stream.Context().Done()
		close(canceled)
		return stream.Context().Err()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	reader, err := a.DownloadFile(ctx, "sandbox", "/file")
	require.NoError(t, err)
	var data [4]byte
	_, err = io.ReadFull(reader, data[:])
	require.NoError(t, err)
	assert.Equal(t, "data", string(data[:]))
	require.NoError(t, reader.Close())
	select {
	case <-canceled:
		require.NoError(t, ctx.Err())
	case <-ctx.Done():
		t.Fatal("closing the download did not cancel the RPC")
	}
}

type failingUploadReader struct {
	ctx     context.Context
	started <-chan struct{}
	err     error
}

func (r failingUploadReader) Read([]byte) (int, error) {
	select {
	case <-r.started:
		return 0, r.err
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	}
}

func TestUploadFile_ReaderFailureCancelsStream(t *testing.T) {
	a, fake := newTestAdapter(t, nil)
	started, canceled := make(chan struct{}), make(chan struct{})
	fake.writeHook = func(stream grpc.ClientStreamingServer[ateenvpb.WriteFileRequest, ateenvpb.WriteFileResponse]) error {
		close(started)
		<-stream.Context().Done()
		close(canceled)
		return stream.Context().Err()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	readErr := errors.New("upload read failed")
	err := a.UploadFile(ctx, "sandbox", &adapter.FileUploadRequest{
		Path: "/file", Reader: io.NopCloser(failingUploadReader{ctx: ctx, started: started, err: readErr}),
	})
	require.ErrorIs(t, err, readErr)
	select {
	case <-canceled:
		require.NoError(t, ctx.Err())
	case <-ctx.Done():
		t.Fatal("upload reader failure did not cancel the RPC")
	}
}

func TestBufferedResultLimits(t *testing.T) {
	a, fake := newTestAdapter(t, nil)
	fake.stdout = strings.Repeat("a", maxCommandOutput/2)
	fake.stderr = strings.Repeat("b", maxCommandOutput/2+1)
	_, err := a.RunCommand(t.Context(), "sandbox", &adapter.CommandRequest{Command: "test"})
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	fake.mu.Lock()
	require.Len(t, fake.signals, 1)
	assert.Equal(t, "p1", fake.signals[0].ProcessId)
	assert.Equal(t, ateenvpb.Signal_SIGNAL_KILL, fake.signals[0].Signal)
	fake.mu.Unlock()

	fake.readHook = func(_ *ateenvpb.ReadFileRequest, stream grpc.ServerStreamingServer[ateenvpb.ReadFileResponse]) error {
		chunk := &ateenvpb.ReadFileResponse{Chunk: make([]byte, 1<<20)}
		for range maxBufferedFileSize/len(chunk.Chunk) + 1 {
			if err := stream.Send(chunk); err != nil {
				return err
			}
		}
		return nil
	}
	_, err = a.ReadFile(t.Context(), "sandbox", "/file")
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
}

func TestAccessToken(t *testing.T) {
	a, _ := newTestAdapter(t, nil)
	ctx := context.Background()
	sbx, err := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{})
	require.NoError(t, err)

	tok, err := a.GetAccessToken(ctx, sbx.SandboxID)
	require.NoError(t, err)
	again, err := a.GetAccessToken(ctx, sbx.SandboxID)
	require.NoError(t, err)
	assert.Equal(t, tok.Token, again.Token)
	assert.True(t, tok.ExpiresAt.Equal(again.ExpiresAt))

	ok, _ := a.ValidateAccessToken(ctx, sbx.SandboxID, tok.Token)
	assert.True(t, ok)
	ok, _ = a.ValidateAccessToken(ctx, sbx.SandboxID, "bogus")
	assert.False(t, ok)

	_, err = a.GetAccessToken(ctx, "sb-missing")
	assert.Error(t, err)

	require.NoError(t, a.KillSandbox(ctx, sbx.SandboxID))
	ok, err = a.ValidateAccessToken(ctx, sbx.SandboxID, tok.Token)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestAccessToken_ConcurrentReplicas(t *testing.T) {
	a, _ := newTestAdapter(t, nil)
	b, err := New(a.cfg)
	require.NoError(t, err)
	store := sessionstore.NewMemory()
	a.SetSessionStore(store)
	b.SetSessionStore(store)

	ctx := t.Context()
	sbx, err := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{})
	require.NoError(t, err)
	type result struct {
		token *adapter.AccessToken
		err   error
	}
	results := make(chan result, 2)
	for _, replica := range []*Adapter{a, b} {
		go func() {
			tok, err := replica.GetAccessToken(ctx, sbx.SandboxID)
			results <- result{tok, err}
		}()
	}
	first, second := <-results, <-results
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	assert.Equal(t, first.token.Token, second.token.Token)
	assert.True(t, first.token.ExpiresAt.Equal(second.token.ExpiresAt))
}

func TestUnsupportedOperations(t *testing.T) {
	a, _ := newTestAdapter(t, nil)
	ctx := context.Background()

	_, _, envdErr := a.GetEnvdEndpoint(ctx, "x")
	_, resumeErr := a.ResumeSandbox(ctx, "x")
	_, portErr := a.GetPortURL(ctx, "x", 80)
	_, buildErr := a.TriggerBuild(ctx, "x", nil)
	_, poolErr := a.CreateWarmPool(ctx, nil)
	for name, err := range map[string]error{
		"pause":     a.PauseSandbox(ctx, "x"),
		"resume":    resumeErr,
		"timeout":   a.SetTimeout(ctx, "x", 0),
		"stream":    a.ExecuteCodeStream(ctx, "x", &adapter.CodeExecutionRequest{}, nil),
		"port":      portErr,
		"build":     buildErr,
		"warm pool": poolErr,
		"envd":      envdErr,
		"process":   a.KillProcess(ctx, "x", "1"),
		"template":  a.DeleteTemplate(ctx, "x"),
	} {
		assert.ErrorIs(t, err, adapter.ErrUnsupported, name)
	}
}

func TestNewAdapterFromConfig(t *testing.T) {
	_, err := NewAdapterFromConfig(config.BackendConfig{Name: "s", Config: map[string]any{}}, nil)
	require.Error(t, err)

	a, err := NewAdapterFromConfig(config.BackendConfig{Name: "s", Config: map[string]any{
		"endpoint":                "localhost:7777",
		"atespace":                "custom",
		"defaulttemplate":         "tpl",
		"templatetoactortemplate": map[string]any{"py": "py-actor"},
	}}, nil)
	require.NoError(t, err)
	sa := a.(*Adapter)
	t.Cleanup(func() { _ = sa.Close() })
	assert.Equal(t, "custom", sa.cfg.Atespace)
	assert.Equal(t, "py-actor", sa.actorTemplate("py"))
	assert.Equal(t, "tpl", sa.actorTemplate(""))
	assert.Equal(t, "other", sa.actorTemplate("other"))

	_, err = NewAdapterFromConfig(config.BackendConfig{Name: "s", Config: map[string]any{
		"endpoint": "localhost:7777",
		"tls":      "true",
	}}, nil)
	require.ErrorContains(t, err, "tls must be a boolean")
}

func TestSandboxStateAcrossReplicas(t *testing.T) {
	a, _ := newTestAdapter(t, func(c *Config) { c.TemplateToActor = map[string]string{"py": "python-actor"} })
	b, err := New(a.cfg)
	require.NoError(t, err)
	b.SetSessionStore(a.sessionStore)
	created, err := a.CreateSandbox(t.Context(), &adapter.CreateSandboxRequest{TemplateID: "py", Alias: "python", Metadata: map[string]string{"team": "dev"}})
	require.NoError(t, err)
	got, err := b.GetSandbox(t.Context(), created.SandboxID)
	require.NoError(t, err)
	assert.Equal(t, created.TemplateID, got.TemplateID)
	assert.Equal(t, created.Alias, got.Alias)
	assert.Equal(t, created.Metadata, got.Metadata)
	require.NoError(t, b.KillSandbox(t.Context(), created.SandboxID))
	_, found, err := a.sessionStore.Get(t.Context(), sandboxKey(created.SandboxID))
	require.NoError(t, err)
	assert.False(t, found)
}

func TestListSandboxesBoundedLookups(t *testing.T) {
	a, fake := newTestAdapter(t, nil)
	for _, id := range []string{"a-missing", "b-crashed", "c-running", "d-running"} {
		a.sandboxes[id] = &sandboxState{sandbox: &adapter.Sandbox{SandboxID: id, Status: adapter.SandboxStatusRunning}}
		if id != "a-missing" {
			fake.envs[id] = &ateenvpb.Environment{Id: id, Status: ateenvpb.EnvironmentStatus_ENVIRONMENT_STATUS_CRASHED}
		}
	}
	page, err := a.ListSandboxes(t.Context(), adapter.ListOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, "b-crashed", page[0].SandboxID)
	assert.Equal(t, adapter.SandboxStatusError, page[0].Status)
	fake.mu.Lock()
	assert.Equal(t, 2, fake.lookups)
	fake.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = a.ListSandboxes(ctx, adapter.ListOptions{})
	require.ErrorIs(t, err, context.Canceled)
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	fake.mu.Lock()
	fake.getHook = func() error { cancel(); return status.Error(codes.Canceled, "request stopped") }
	fake.mu.Unlock()
	_, err = a.ListSandboxes(ctx, adapter.ListOptions{})
	require.ErrorIs(t, err, context.Canceled)
}

func TestLabelPresenceAndFindPaths(t *testing.T) {
	assert.False(t, matchesLabels(nil, map[string]string{"team": ""}))
	assert.True(t, matchesLabels(map[string]string{"team": ""}, map[string]string{"team": ""}))
	for _, path := range []string{"!", "(", "-dir"} {
		assert.Equal(t, "./"+path, safeArg(path))
	}
	for _, path := range []string{"!dir", "(dir", "normal", "/tmp"} {
		assert.Equal(t, path, safeArg(path))
	}
}

type failedWriteStore struct {
	sessionstore.Store
	prefix string
}

func (s failedWriteStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if err := s.Store.Set(ctx, key, value, ttl); err != nil {
		return err
	}
	if strings.HasPrefix(key, s.prefix) {
		return errors.New("write reply lost")
	}
	return nil
}

func TestCreateSandbox_RollsBackCommittedStoreWrites(t *testing.T) {
	for _, prefix := range []string{"routing:", "agentsubstrate:sandbox:"} {
		t.Run(prefix, func(t *testing.T) {
			store := failedWriteStore{Store: sessionstore.NewMemory(), prefix: prefix}
			a, fake := newTestAdapter(t, func(c *Config) { c.Registry = adapter.NewRegistry(store, time.Hour) })
			a.SetSessionStore(store)
			_, err := a.CreateSandbox(t.Context(), &adapter.CreateSandboxRequest{TemplateID: "base"})
			require.ErrorContains(t, err, "write reply lost")
			fake.mu.Lock()
			assert.Empty(t, fake.envs)
			fake.mu.Unlock()
			assert.Zero(t, a.cfg.Registry.SandboxBackend().Size())
			var keys []string
			require.NoError(t, store.Scan(t.Context(), "", func(key, value string) { keys = append(keys, key) }))
			assert.Empty(t, keys)
		})
	}
}
