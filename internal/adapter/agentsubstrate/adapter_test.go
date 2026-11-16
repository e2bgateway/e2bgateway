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
	"sync"
	"testing"

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
)

type fakeServer struct {
	ateenvpb.UnimplementedEnvironmentServiceServer
	ateenvpb.UnimplementedProcessServiceServer
	ateenvpb.UnimplementedFileSystemServiceServer

	mu       sync.Mutex
	envs     map[string]*ateenvpb.Environment
	files    map[string][]byte
	started  []*ateenvpb.StartProcessRequest
	envMeta  []string
	stdout   string
	stderr   string
	exitCode int32
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

	cfg := Config{Name: "substrate", Conn: conn, Registry: adapter.NewRegistry()}
	if mutate != nil {
		mutate(&cfg)
	}
	a, err := New(cfg)
	require.NoError(t, err)
	return a, fake
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
	assert.Equal(t, "python-actor", fake.envs[sbx.SandboxID].Template.Name)
	assert.Equal(t, DefaultAtespace, fake.envs[sbx.SandboxID].Atespace)
	owner, _ := a.cfg.Registry.SandboxBackend().Get(sbx.SandboxID)
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

	require.NoError(t, a.KillSandbox(ctx, sbx.SandboxID))
	_, err = a.GetSandbox(ctx, sbx.SandboxID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	assert.Error(t, a.KillSandbox(ctx, sbx.SandboxID))
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

	require.NoError(t, a.UploadFile(ctx, sbx.SandboxID, &adapter.FileUploadRequest{
		Path: "/empty", Reader: io.NopCloser(bytes.NewReader(nil)),
	}))
	assert.Contains(t, fake.files, "/empty")

	_, err = a.ReadFile(ctx, sbx.SandboxID, "/missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

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

	ok, _ := a.ValidateAccessToken(ctx, sbx.SandboxID, tok.Token)
	assert.True(t, ok)
	ok, _ = a.ValidateAccessToken(ctx, sbx.SandboxID, "bogus")
	assert.False(t, ok)

	_, err = a.GetAccessToken(ctx, "sb-missing")
	assert.Error(t, err)
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
}
