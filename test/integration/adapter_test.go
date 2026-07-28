package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	testmock "github.com/e2bgateway/e2bgateway/test/mock"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// createSandbox is a test helper that creates a sandbox and fails the test on
// error.
func createSandbox(t *testing.T, backend *testmock.MockBackend, templateID string) *adapter.Sandbox {
	t.Helper()
	sbx, err := backend.CreateSandbox(context.Background(), &adapter.CreateSandboxRequest{
		TemplateID: templateID,
		Timeout:    300,
	})
	if err != nil {
		t.Fatalf("CreateSandbox(%s): %v", templateID, err)
	}
	if sbx == nil {
		t.Fatal("CreateSandbox returned nil sandbox")
	}
	if sbx.SandboxID == "" {
		t.Fatal("CreateSandbox returned empty SandboxID")
	}
	return sbx
}

// ---------------------------------------------------------------------------
// TestSandboxLifecycle – Create → List → Get → Pause → Resume → Kill
// ---------------------------------------------------------------------------

func TestSandboxLifecycle(t *testing.T) {
	backend := testmock.NewMockBackend()
	ctx := context.Background()

	// 1. Create
	sbx := createSandbox(t, backend, "base")
	t.Logf("created sandbox %s (template=%s)", sbx.SandboxID, sbx.TemplateID)

	if sbx.Status != adapter.SandboxStatusRunning {
		t.Fatalf("expected status running, got %s", sbx.Status)
	}

	// 2. List
	list, err := backend.ListSandboxes(ctx, adapter.ListOptions{})
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 sandbox, got %d", len(list))
	}

	// 3. Get
	got, err := backend.GetSandbox(ctx, sbx.SandboxID)
	if err != nil {
		t.Fatalf("GetSandbox: %v", err)
	}
	if got.SandboxID != sbx.SandboxID {
		t.Fatalf("GetSandbox returned wrong ID: want %s, got %s", sbx.SandboxID, got.SandboxID)
	}

	// 4. Pause
	if err := backend.PauseSandbox(ctx, sbx.SandboxID); err != nil {
		t.Fatalf("PauseSandbox: %v", err)
	}
	paused, _ := backend.GetSandbox(ctx, sbx.SandboxID)
	if paused.Status != adapter.SandboxStatusPaused {
		t.Fatalf("expected paused, got %s", paused.Status)
	}

	// 5. Resume
	resumed, err := backend.ResumeSandbox(ctx, sbx.SandboxID)
	if err != nil {
		t.Fatalf("ResumeSandbox: %v", err)
	}
	if resumed.Status != adapter.SandboxStatusRunning {
		t.Fatalf("expected running after resume, got %s", resumed.Status)
	}

	// 6. SetTimeout
	if err := backend.SetTimeout(ctx, sbx.SandboxID, 10*time.Minute); err != nil {
		t.Fatalf("SetTimeout: %v", err)
	}

	// 7. Kill
	if err := backend.KillSandbox(ctx, sbx.SandboxID); err != nil {
		t.Fatalf("KillSandbox: %v", err)
	}

	// 8. Verify gone
	_, err = backend.GetSandbox(ctx, sbx.SandboxID)
	if err == nil {
		t.Fatal("expected error getting killed sandbox, got nil")
	}

	list, _ = backend.ListSandboxes(ctx, adapter.ListOptions{})
	if len(list) != 0 {
		t.Fatalf("expected 0 sandboxes after kill, got %d", len(list))
	}

	// 9. Verify call tracking recorded every step.
	assertCallCount := func(method string, want int) {
		t.Helper()
		if got := backend.CallCount(method); got != want {
			t.Errorf("CallCount(%s) = %d, want %d", method, got, want)
		}
	}
	assertCallCount("CreateSandbox", 1)
	assertCallCount("ListSandboxes", 2)
	assertCallCount("GetSandbox", 3)   // get + pause-verify + post-kill verify
	assertCallCount("PauseSandbox", 1)
	assertCallCount("ResumeSandbox", 1)
	assertCallCount("SetTimeout", 1)
	assertCallCount("KillSandbox", 1)
}

// ---------------------------------------------------------------------------
// TestConcurrentSandboxOperations – thread safety under parallel load
// ---------------------------------------------------------------------------

func TestConcurrentSandboxOperations(t *testing.T) {
	backend := testmock.NewMockBackend()
	ctx := context.Background()

	const goroutines = 50
	var wg sync.WaitGroup
	sandboxIDs := make([]string, goroutines)

	// Phase 1 – concurrent creates.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sbx, err := backend.CreateSandbox(ctx, &adapter.CreateSandboxRequest{
				TemplateID: "base",
			})
			if err != nil {
				t.Errorf("goroutine %d: CreateSandbox: %v", idx, err)
				return
			}
			sandboxIDs[idx] = sbx.SandboxID
		}(i)
	}
	wg.Wait()

	// Verify all created.
	list, err := backend.ListSandboxes(ctx, adapter.ListOptions{})
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if len(list) != goroutines {
		t.Fatalf("expected %d sandboxes, got %d", goroutines, len(list))
	}

	// Phase 2 – concurrent reads/writes on different sandboxes.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id := sandboxIDs[idx]

			_, err := backend.GetSandbox(ctx, id)
			if err != nil {
				t.Errorf("goroutine %d: GetSandbox: %v", idx, err)
			}

			_, err = backend.ExecuteCode(ctx, id, &adapter.CodeExecutionRequest{
				Code:     fmt.Sprintf("print(%d)", idx),
				Language: "python",
			})
			if err != nil {
				t.Errorf("goroutine %d: ExecuteCode: %v", idx, err)
			}

			err = backend.WriteFile(ctx, id, &adapter.FileWriteRequest{
				Path:    "/tmp/test.txt",
				Content: []byte(fmt.Sprintf("content-%d", idx)),
			})
			if err != nil {
				t.Errorf("goroutine %d: WriteFile: %v", idx, err)
			}

			_, err = backend.ReadFile(ctx, id, "/tmp/test.txt")
			if err != nil {
				t.Errorf("goroutine %d: ReadFile: %v", idx, err)
			}

			_, err = backend.ListPorts(ctx, id)
			if err != nil {
				t.Errorf("goroutine %d: ListPorts: %v", idx, err)
			}

			_, err = backend.GetAccessToken(ctx, id)
			if err != nil {
				t.Errorf("goroutine %d: GetAccessToken: %v", idx, err)
			}
		}(i)
	}
	wg.Wait()

	// All 50 goroutines touched these methods.
	if c := backend.CallCount("ExecuteCode"); c != goroutines {
		t.Errorf("ExecuteCode call count = %d, want %d", c, goroutines)
	}
	if c := backend.CallCount("WriteFile"); c != goroutines {
		t.Errorf("WriteFile call count = %d, want %d", c, goroutines)
	}
}

// ---------------------------------------------------------------------------
// TestAdapterErrorHandling – error propagation through the adapter interface
// ---------------------------------------------------------------------------

func TestAdapterErrorHandling(t *testing.T) {
	backend := testmock.NewMockBackend()
	ctx := context.Background()

	sentinel := errors.New("test-error")

	// --- Sandbox not found ---
	if _, err := backend.GetSandbox(ctx, "nonexistent"); err == nil {
		t.Error("expected error for GetSandbox(nonexistent), got nil")
	}
	if err := backend.KillSandbox(ctx, "nonexistent"); err == nil {
		t.Error("expected error for KillSandbox(nonexistent), got nil")
	}
	if err := backend.PauseSandbox(ctx, "nonexistent"); err == nil {
		t.Error("expected error for PauseSandbox(nonexistent), got nil")
	}
	if _, err := backend.ResumeSandbox(ctx, "nonexistent"); err == nil {
		t.Error("expected error for ResumeSandbox(nonexistent), got nil")
	}

	// --- Template not found ---
	if _, err := backend.GetTemplate(ctx, "nonexistent"); err == nil {
		t.Error("expected error for GetTemplate(nonexistent), got nil")
	}
	if err := backend.DeleteTemplate(ctx, "nonexistent"); err == nil {
		t.Error("expected error for DeleteTemplate(nonexistent), got nil")
	}
	if err := backend.CreateAlias(ctx, "nonexistent", "a"); err == nil {
		t.Error("expected error for CreateAlias on missing template, got nil")
	}

	// --- Configured error injection ---
	backend.GetSandboxFn = func(ctx context.Context, id string) (*adapter.Sandbox, error) {
		return nil, sentinel
	}
	if _, err := backend.GetSandbox(ctx, "any"); !errors.Is(err, sentinel) {
		t.Errorf("GetSandbox: expected sentinel error, got %v", err)
	}
	backend.GetSandboxFn = nil // reset

	backend.ExecuteCodeFn = func(ctx context.Context, id string, req *adapter.CodeExecutionRequest) (*adapter.CodeExecutionResult, error) {
		return nil, sentinel
	}
	// Need an existing sandbox for the wrapper to not short-circuit; but since
	// the Fn is set it runs unconditionally.
	if _, err := backend.ExecuteCode(ctx, "any", &adapter.CodeExecutionRequest{Code: "x"}); !errors.Is(err, sentinel) {
		t.Errorf("ExecuteCode: expected sentinel error, got %v", err)
	}
	backend.ExecuteCodeFn = nil

	backend.HealthCheckFn = func(ctx context.Context) error {
		return sentinel
	}
	if err := backend.HealthCheck(ctx); !errors.Is(err, sentinel) {
		t.Errorf("HealthCheck: expected sentinel error, got %v", err)
	}
	backend.HealthCheckFn = nil

	backend.WriteFileFn = func(ctx context.Context, id string, req *adapter.FileWriteRequest) error {
		return sentinel
	}
	if err := backend.WriteFile(ctx, "any", &adapter.FileWriteRequest{Path: "/x"}); !errors.Is(err, sentinel) {
		t.Errorf("WriteFile: expected sentinel error, got %v", err)
	}
	backend.WriteFileFn = nil

	backend.DeleteTemplateFn = func(ctx context.Context, id string) error {
		return sentinel
	}
	if err := backend.DeleteTemplate(ctx, "any"); !errors.Is(err, sentinel) {
		t.Errorf("DeleteTemplate: expected sentinel error, got %v", err)
	}
	backend.DeleteTemplateFn = nil

	backend.ListSandboxesFn = func(ctx context.Context, opts adapter.ListOptions) ([]*adapter.Sandbox, error) {
		return nil, sentinel
	}
	if _, err := backend.ListSandboxes(ctx, adapter.ListOptions{}); !errors.Is(err, sentinel) {
		t.Errorf("ListSandboxes: expected sentinel error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestCodeExecutionThroughAdapter – code execution workflow
// ---------------------------------------------------------------------------

func TestCodeExecutionThroughAdapter(t *testing.T) {
	backend := testmock.NewMockBackend()
	ctx := context.Background()

	// Install custom code execution behavior.
	backend.ExecuteCodeFn = func(ctx context.Context, id string, req *adapter.CodeExecutionRequest) (*adapter.CodeExecutionResult, error) {
		return &adapter.CodeExecutionResult{
			Stdout:   "hello from " + req.Language,
			Stderr:   "",
			ExitCode: 0,
			Duration: 42 * time.Millisecond,
		}, nil
	}

	sbx := createSandbox(t, backend, "code-interpreter")

	// Execute Python code.
	result, err := backend.ExecuteCode(ctx, sbx.SandboxID, &adapter.CodeExecutionRequest{
		Code:     "print('hello')",
		Language: "python",
	})
	if err != nil {
		t.Fatalf("ExecuteCode: %v", err)
	}
	if result.Stdout != "hello from python" {
		t.Errorf("stdout = %q, want %q", result.Stdout, "hello from python")
	}
	if result.ExitCode != 0 {
		t.Errorf("exitCode = %d, want 0", result.ExitCode)
	}

	// Execute JavaScript code.
	result, err = backend.ExecuteCode(ctx, sbx.SandboxID, &adapter.CodeExecutionRequest{
		Code:     "console.log('hi')",
		Language: "javascript",
	})
	if err != nil {
		t.Fatalf("ExecuteCode(javascript): %v", err)
	}
	if result.Stdout != "hello from javascript" {
		t.Errorf("stdout = %q, want %q", result.Stdout, "hello from javascript")
	}

	// RunCommand.
	cmdResult, err := backend.RunCommand(ctx, sbx.SandboxID, &adapter.CommandRequest{
		Command: "echo",
		Args:    []string{"hello"},
	})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if cmdResult.ExitCode != 0 {
		t.Errorf("RunCommand exitCode = %d, want 0", cmdResult.ExitCode)
	}

	// ExecuteCodeStream
	streamCalled := false
	backend.ExecuteCodeStreamFn = func(ctx context.Context, id string, req *adapter.CodeExecutionRequest, stream adapter.CodeStream) error {
		streamCalled = true
		_ = stream.Send(&adapter.StreamMessage{Type: "stdout", Data: "line1"})
		_ = stream.Send(&adapter.StreamMessage{Type: "result", Data: "done"})
		return stream.Close()
	}
	// Use a no-op CodeStream implementation.
	err = backend.ExecuteCodeStream(ctx, sbx.SandboxID, &adapter.CodeExecutionRequest{Code: "x"}, &noopStream{})
	if err != nil {
		t.Fatalf("ExecuteCodeStream: %v", err)
	}
	if !streamCalled {
		t.Error("ExecuteCodeStreamFn was not called")
	}

	if c := backend.CallCount("ExecuteCode"); c != 2 {
		t.Errorf("ExecuteCode call count = %d, want 2", c)
	}
	if c := backend.CallCount("RunCommand"); c != 1 {
		t.Errorf("RunCommand call count = %d, want 1", c)
	}
	if c := backend.CallCount("ExecuteCodeStream"); c != 1 {
		t.Errorf("ExecuteCodeStream call count = %d, want 1", c)
	}
}

// noopStream is a minimal CodeStream for tests that just need to satisfy the
// interface without inspecting messages.
type noopStream struct{}

func (s *noopStream) Send(_ *adapter.StreamMessage) error { return nil }
func (s *noopStream) Close() error                        { return nil }

// ---------------------------------------------------------------------------
// TestFileOperationsThroughAdapter – file CRUD
// ---------------------------------------------------------------------------

func TestFileOperationsThroughAdapter(t *testing.T) {
	backend := testmock.NewMockBackend()
	ctx := context.Background()

	sbx := createSandbox(t, backend, "base")
	id := sbx.SandboxID

	// WriteFile
	err := backend.WriteFile(ctx, id, &adapter.FileWriteRequest{
		Path:    "/home/user/hello.py",
		Content: []byte("print('hello world')"),
	})
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// ReadFile
	fc, err := backend.ReadFile(ctx, id, "/home/user/hello.py")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(fc.Content) != "print('hello world')" {
		t.Errorf("ReadFile content = %q, want %q", string(fc.Content), "print('hello world')")
	}
	if fc.Size != int64(len("print('hello world')")) {
		t.Errorf("ReadFile size = %d, want %d", fc.Size, len("print('hello world')"))
	}

	// UploadFile (nil reader is fine for the mock – it only validates sandbox existence)
	err = backend.UploadFile(ctx, id, &adapter.FileUploadRequest{
		Path:   "/home/user/uploaded.txt",
		Reader: io.NopCloser(strings.NewReader("uploaded-content")),
	})
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}

	// ListFiles – should see at least the file we wrote.
	files, err := backend.ListFiles(ctx, id, "/home/user")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) < 1 {
		t.Fatal("ListFiles returned 0 entries, expected at least 1")
	}
	foundWritten := false
	for _, fi := range files {
		if fi.Path == "/home/user/hello.py" {
			foundWritten = true
		}
	}
	if !foundWritten {
		t.Errorf("written file /home/user/hello.py not found in listing: %+v", files)
	}

	// MakeDir
	if err := backend.MakeDir(ctx, id, "/home/user/newdir"); err != nil {
		t.Fatalf("MakeDir: %v", err)
	}

	// MoveFile
	err = backend.WriteFile(ctx, id, &adapter.FileWriteRequest{
		Path:    "/tmp/src.txt",
		Content: []byte("move-me"),
	})
	if err != nil {
		t.Fatalf("WriteFile (pre-move): %v", err)
	}
	if err := backend.MoveFile(ctx, id, "/tmp/src.txt", "/tmp/dst.txt"); err != nil {
		t.Fatalf("MoveFile: %v", err)
	}

	// RemoveFile – remove the moved file.
	if err := backend.RemoveFile(ctx, id, "/tmp/dst.txt"); err != nil {
		t.Fatalf("RemoveFile: %v", err)
	}

	// DownloadFile – our wrapper reads via ReadFile, so the file we wrote
	// earlier should come back as a readable stream.
	rc, err := backend.DownloadFile(ctx, id, "/home/user/hello.py")
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "print('hello world')" {
		t.Errorf("DownloadFile content = %q, want %q", string(data), "print('hello world')")
	}

	// Verify call counts.
	if c := backend.CallCount("WriteFile"); c != 2 {
		t.Errorf("WriteFile call count = %d, want 2", c)
	}
	if c := backend.CallCount("ReadFile"); c != 1 {
		t.Errorf("ReadFile call count = %d, want 1", c)
	}
	if c := backend.CallCount("UploadFile"); c != 1 {
		t.Errorf("UploadFile call count = %d, want 1", c)
	}
	if c := backend.CallCount("ListFiles"); c != 1 {
		t.Errorf("ListFiles call count = %d, want 1", c)
	}
	if c := backend.CallCount("MakeDir"); c != 1 {
		t.Errorf("MakeDir call count = %d, want 1", c)
	}
	if c := backend.CallCount("MoveFile"); c != 1 {
		t.Errorf("MoveFile call count = %d, want 1", c)
	}
	if c := backend.CallCount("RemoveFile"); c != 1 {
		t.Errorf("RemoveFile call count = %d, want 1", c)
	}
	if c := backend.CallCount("DownloadFile"); c != 1 {
		t.Errorf("DownloadFile call count = %d, want 1", c)
	}
}
