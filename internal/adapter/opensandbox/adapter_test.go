package opensandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
	"github.com/e2bgateway/e2bgateway/internal/adapter"
)

// ---------------------------------------------------------------------------
// Fake OpenSandbox server that speaks the real server's wire protocol.
// ---------------------------------------------------------------------------

type fakeSandbox struct {
	id        string
	imageURI  string
	state     opensandbox.SandboxState
	createdAt time.Time
	metadata  map[string]string
}

// fakeServer is a minimal, in-memory OpenSandbox server that implements just
// enough of the lifecycle and execd API for the adapter to work end-to-end.
type fakeServer struct {
	mu        sync.Mutex
	sandboxes map[string]*fakeSandbox
	files     map[string]map[string][]byte // sandboxID -> path -> contents
	t         *testing.T
	nextID    int
}

func newFakeServer(t *testing.T) *fakeServer {
	return &fakeServer{
		sandboxes: make(map[string]*fakeSandbox),
		files:     make(map[string]map[string][]byte),
		t:         t,
	}
}

func (f *fakeServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		path := r.URL.Path
		w.Header().Set("Content-Type", "application/json")

		// POST /sandboxes -> create
		if r.Method == http.MethodPost && path == "/sandboxes" {
			var req opensandbox.CreateSandboxRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req.ResourceLimits == nil {
				http.Error(w, "resourceLimits is required", http.StatusBadRequest)
				return
			}
			f.nextID++
			id := fmt.Sprintf("osbx-%d", f.nextID)
			f.sandboxes[id] = &fakeSandbox{
				id:        id,
				imageURI:  req.Image.URI,
				state:     opensandbox.StateRunning, // already running in tests
				createdAt: time.Now(),
				metadata:  req.Metadata,
			}
			f.files[id] = make(map[string][]byte)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id": id,
				"status": map[string]interface{}{
					"state": string(opensandbox.StateRunning),
				},
				"image":      map[string]interface{}{"uri": req.Image.URI},
				"createdAt":  f.sandboxes[id].createdAt,
				"entrypoint": req.Entrypoint,
				"metadata":   req.Metadata,
			})
			return
		}

		// GET /sandboxes -> list
		if r.Method == http.MethodGet && path == "/sandboxes" {
			items := make([]map[string]interface{}, 0, len(f.sandboxes))
			for _, s := range f.sandboxes {
				items = append(items, map[string]interface{}{
					"id": s.id,
					"status": map[string]interface{}{
						"state": string(s.state),
					},
					"image":     map[string]interface{}{"uri": s.imageURI},
					"createdAt": s.createdAt,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"items": items,
			})
			return
		}

		// Route: /sandboxes/{id}/*
		if strings.HasPrefix(path, "/sandboxes/") {
			rest := strings.TrimPrefix(path, "/sandboxes/")
			parts := strings.SplitN(rest, "/", 2)
			id := parts[0]
			sub := ""
			if len(parts) == 2 {
				sub = parts[1]
			}
			sbx, ok := f.sandboxes[id]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}

			switch {
			case sub == "" && r.Method == http.MethodGet:
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"id": sbx.id,
					"status": map[string]interface{}{
						"state": string(sbx.state),
					},
					"image":     map[string]interface{}{"uri": sbx.imageURI},
					"createdAt": sbx.createdAt,
				})
				return

			case sub == "" && r.Method == http.MethodDelete:
				delete(f.sandboxes, id)
				delete(f.files, id)
				w.WriteHeader(http.StatusNoContent)
				return

			case sub == "pause" && r.Method == http.MethodPost:
				if sbx.state != opensandbox.StateRunning {
					http.Error(w, "not running", http.StatusConflict)
					return
				}
				sbx.state = opensandbox.StatePaused
				w.WriteHeader(http.StatusAccepted)
				return

			case sub == "resume" && r.Method == http.MethodPost:
				if sbx.state != opensandbox.StatePaused {
					http.Error(w, "not paused", http.StatusConflict)
					return
				}
				sbx.state = opensandbox.StateRunning
				w.WriteHeader(http.StatusAccepted)
				return

			case sub == "renew-expiration" && r.Method == http.MethodPost:
				w.WriteHeader(http.StatusNoContent)
				return

			// GET /sandboxes/{id}/endpoints/{port}
			case strings.HasPrefix(sub, "endpoints/") && r.Method == http.MethodGet:
				port := strings.TrimPrefix(sub, "endpoints/")
				useProxy := r.URL.Query().Get("use_server_proxy") == "true"
				endpoint := fmt.Sprintf("127.0.0.1:%s", port)
				if useProxy {
					// Strip scheme: the SDK will prepend http:// if missing.
					endpoint = fmt.Sprintf("%s/sandboxes/%s/proxy/%s",
						strings.TrimPrefix(r.Host, "http://"), id, port)
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"endpoint": endpoint,
				})
				return

			// Proxied execd calls: /sandboxes/{id}/proxy/{port}/*
			case strings.HasPrefix(sub, "proxy/"):
				// Strip /proxy/{port} to get the inner execd path.
				afterProxy := strings.TrimPrefix(sub, "proxy/")
				slash := strings.Index(afterProxy, "/")
				innerPath := "/"
				if slash >= 0 {
					innerPath = afterProxy[slash:]
				}
				f.handleExecd(w, r, id, innerPath)
				return
			}
		}

		http.Error(w, "not found: "+r.Method+" "+path, http.StatusNotFound)
	}
}

// handleExecd simulates the execd daemon running inside a sandbox.
func (f *fakeServer) handleExecd(w http.ResponseWriter, r *http.Request, sandboxID, path string) {
	switch {
	// POST /command — streaming SSE
	case path == "/command" && r.Method == http.MethodPost:
		var req opensandbox.RunCommandRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		// Simple simulated output: echo the command back as stdout.
		fmt.Fprintf(w, "event: stdout\ndata: %s\n\n", req.Command)
		fmt.Fprintf(w, "event: result\ndata: {\"exit_code\":0}\n\n")
		if ok {
			flusher.Flush()
		}
		return

	// POST /files/upload — multipart. The SDK sends paired parts:
	// a "metadata" part (Content-Disposition: form-data; name="metadata"; filename="metadata")
	// followed by a "file" part (name="file"; filename=...). Because the metadata
	// part also has a filename, Go's ParseMultipartForm puts it in .File (not .Value).
	case path == "/files/upload" && r.Method == http.MethodPost:
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		metaParts := r.MultipartForm.File["metadata"]
		fileParts := r.MultipartForm.File["file"]
		if len(metaParts) == 0 || len(fileParts) == 0 {
			http.Error(w, "missing metadata or file part", http.StatusBadRequest)
			return
		}
		for i, fh := range fileParts {
			metaFH := metaParts[0]
			if i < len(metaParts) {
				metaFH = metaParts[i]
			}
			mf, err := metaFH.Open()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			metaBytes, _ := io.ReadAll(mf)
			mf.Close()
			var meta struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(metaBytes, &meta)
			ff, err := fh.Open()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			data, _ := io.ReadAll(ff)
			ff.Close()
			f.files[sandboxID][meta.Path] = data
		}
		w.WriteHeader(http.StatusNoContent)
		return

	// GET /files/download?path=...
	case path == "/files/download" && r.Method == http.MethodGet:
		p := r.URL.Query().Get("path")
		data, ok := f.files[sandboxID][p]
		if !ok {
			http.Error(w, "no such file", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		return

	// GET /directories/list?path=...
	case path == "/directories/list" && r.Method == http.MethodGet:
		dir := r.URL.Query().Get("path")
		prefix := dir
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		seen := map[string]bool{}
		entries := make([]map[string]interface{}, 0)
		for p, data := range f.files[sandboxID] {
			if !strings.HasPrefix(p, prefix) || p == dir {
				continue
			}
			rest := strings.TrimPrefix(p, prefix)
			name := rest
			isDir := false
			if idx := strings.Index(rest, "/"); idx >= 0 {
				name = rest[:idx]
				isDir = true
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			entry := map[string]interface{}{
				"path": prefix + name,
				"size": 0,
				"mode": 644,
			}
			if isDir {
				entry["type"] = "directory"
			} else {
				entry["type"] = "file"
				if data != nil {
					entry["size"] = len(data)
				}
			}
			entries = append(entries, entry)
		}
		_ = json.NewEncoder(w).Encode(entries)
		return

	// POST /directories — CreateDirectory. Body: { "/path": {"mode": 755} }
	case path == "/directories" && r.Method == http.MethodPost:
		var req map[string]map[string]int
		_ = json.NewDecoder(r.Body).Decode(&req)
		if f.files[sandboxID] == nil {
			f.files[sandboxID] = make(map[string][]byte)
		}
		for p := range req {
			// Directory sentinel (nil body).
			f.files[sandboxID][p] = nil
		}
		w.WriteHeader(http.StatusNoContent)
		return

	// DELETE /files?path=...&path=... — DeleteFiles
	case path == "/files" && r.Method == http.MethodDelete:
		paths := r.URL.Query()["path"]
		for _, p := range paths {
			delete(f.files[sandboxID], p)
		}
		w.WriteHeader(http.StatusNoContent)
		return

	// POST /files/mv — MoveFiles
	case path == "/files/mv" && r.Method == http.MethodPost:
		var req []struct {
			Src  string `json:"src"`
			Dest string `json:"dest"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		for _, m := range req {
			data, ok := f.files[sandboxID][m.Src]
			if !ok {
				continue
			}
			delete(f.files[sandboxID], m.Src)
			f.files[sandboxID][m.Dest] = data
		}
		w.WriteHeader(http.StatusNoContent)
		return

	// POST /code/context (ExecuteCode uses a context)
	case path == "/code/context" && r.Method == http.MethodPost:
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":       "ctx-1",
			"language": "python",
		})
		return

	// POST /code — streaming SSE for ExecuteCode
	case path == "/code" && r.Method == http.MethodPost:
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "event: stdout\ndata: executed\n\n")
		fmt.Fprintf(w, "event: result\ndata: {\"exit_code\":0}\n\n")
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		return
	}

	http.Error(w, "execd not found: "+path, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// Helper to build an adapter wired to a fake server.
// ---------------------------------------------------------------------------

func newTestAdapter(t *testing.T) (*Adapter, *fakeServer, func()) {
	t.Helper()
	srv := newFakeServer(t)
	ts := httptest.NewServer(srv.handler())
	a, err := New(AdapterConfig{
		Name:    "test",
		BaseURL: ts.URL,
	})
	if err != nil {
		ts.Close()
		t.Fatalf("New() error: %v", err)
	}
	return a, srv, func() { ts.Close() }
}

// ---------------------------------------------------------------------------
// Lifecycle tests
// ---------------------------------------------------------------------------

func TestCreateSandbox(t *testing.T) {
	a, srv, cleanup := newTestAdapter(t)
	defer cleanup()

	got, err := a.CreateSandbox(context.Background(), &adapter.CreateSandboxRequest{
		TemplateID: "python:3.11",
		Metadata:   map[string]string{"tenant": "unit-test"},
	})
	if err != nil {
		t.Fatalf("CreateSandbox error: %v", err)
	}
	if got.SandboxID == "" {
		t.Error("SandboxID is empty")
	}
	if got.TemplateID != "python:3.11" {
		t.Errorf("TemplateID = %q, want %q", got.TemplateID, "python:3.11")
	}
	if got.Status != adapter.SandboxStatusRunning {
		t.Errorf("Status = %q, want %q", got.Status, adapter.SandboxStatusRunning)
	}
	if got.Backend != "test" {
		t.Errorf("Backend = %q, want %q", got.Backend, "test")
	}
	if len(srv.sandboxes) != 1 {
		t.Errorf("fake has %d sandboxes, want 1", len(srv.sandboxes))
	}
}

func TestCreateSandbox_ResourceLimitsRequired(t *testing.T) {
	// Sanity: if the server rejects a create, the adapter bubbles up the error.
	// Our fake enforces resourceLimits presence — but the adapter always sets
	// DefaultResourceLimits, so this instead verifies the round-trip succeeds.
	a, _, cleanup := newTestAdapter(t)
	defer cleanup()

	got, err := a.CreateSandbox(context.Background(), &adapter.CreateSandboxRequest{
		TemplateID: "alpine:3.20",
	})
	if err != nil {
		t.Fatalf("CreateSandbox error: %v", err)
	}
	if got.SandboxID == "" {
		t.Error("SandboxID is empty")
	}
}

func TestListAndGetSandbox(t *testing.T) {
	a, _, cleanup := newTestAdapter(t)
	defer cleanup()
	ctx := context.Background()

	s1, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "img-a"})
	s2, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "img-b"})

	list, err := a.ListSandboxes(ctx, adapter.ListOptions{})
	if err != nil {
		t.Fatalf("ListSandboxes error: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}

	got, err := a.GetSandbox(ctx, s1.SandboxID)
	if err != nil {
		t.Fatalf("GetSandbox error: %v", err)
	}
	if got.SandboxID != s1.SandboxID {
		t.Errorf("SandboxID = %q, want %q", got.SandboxID, s1.SandboxID)
	}
	if got.TemplateID != "img-a" {
		t.Errorf("TemplateID = %q, want %q", got.TemplateID, "img-a")
	}

	// Get on unknown sandbox should error.
	if _, err := a.GetSandbox(ctx, "does-not-exist"); err == nil {
		t.Error("GetSandbox(unknown) expected error, got nil")
	}
	_ = s2
}

func TestKillPauseResumeSandbox(t *testing.T) {
	a, _, cleanup := newTestAdapter(t)
	defer cleanup()
	ctx := context.Background()

	s, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "img"})

	if err := a.PauseSandbox(ctx, s.SandboxID); err != nil {
		t.Fatalf("PauseSandbox error: %v", err)
	}
	got, _ := a.GetSandbox(ctx, s.SandboxID)
	if got.Status != adapter.SandboxStatusPaused {
		t.Errorf("after pause, status = %q, want %q", got.Status, adapter.SandboxStatusPaused)
	}

	resumed, err := a.ResumeSandbox(ctx, s.SandboxID)
	if err != nil {
		t.Fatalf("ResumeSandbox error: %v", err)
	}
	if resumed.Status != adapter.SandboxStatusRunning {
		t.Errorf("after resume, status = %q, want %q", resumed.Status, adapter.SandboxStatusRunning)
	}

	if err := a.KillSandbox(ctx, s.SandboxID); err != nil {
		t.Fatalf("KillSandbox error: %v", err)
	}
	if _, err := a.GetSandbox(ctx, s.SandboxID); err == nil {
		t.Error("GetSandbox after Kill expected error, got nil")
	}
}

func TestSetTimeout(t *testing.T) {
	a, _, cleanup := newTestAdapter(t)
	defer cleanup()
	ctx := context.Background()

	s, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "img"})
	if err := a.SetTimeout(ctx, s.SandboxID, 5*time.Minute); err != nil {
		t.Fatalf("SetTimeout error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Code/command execution
// ---------------------------------------------------------------------------

func TestRunCommand(t *testing.T) {
	a, _, cleanup := newTestAdapter(t)
	defer cleanup()
	ctx := context.Background()

	s, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "img"})

	res, err := a.RunCommand(ctx, s.SandboxID, &adapter.CommandRequest{
		Command: "echo hello",
	})
	if err != nil {
		t.Fatalf("RunCommand error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "echo hello") {
		t.Errorf("Stdout = %q, want it to echo the command", res.Stdout)
	}
}

func TestRunCommand_WithArgs(t *testing.T) {
	a, _, cleanup := newTestAdapter(t)
	defer cleanup()
	ctx := context.Background()

	s, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "img"})

	res, err := a.RunCommand(ctx, s.SandboxID, &adapter.CommandRequest{
		Command: "ls",
		Args:    []string{"-la", "/tmp"},
	})
	if err != nil {
		t.Fatalf("RunCommand error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	// The fake echoes the full concatenated command.
	if !strings.Contains(res.Stdout, "ls -la /tmp") {
		t.Errorf("Stdout = %q, want it to include args", res.Stdout)
	}
}

func TestExecuteCode(t *testing.T) {
	a, _, cleanup := newTestAdapter(t)
	defer cleanup()
	ctx := context.Background()

	s, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "img"})

	res, err := a.ExecuteCode(ctx, s.SandboxID, &adapter.CodeExecutionRequest{
		Code:     "print('hi')",
		Language: "python",
	})
	if err != nil {
		t.Fatalf("ExecuteCode error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
}

// ---------------------------------------------------------------------------
// Filesystem
// ---------------------------------------------------------------------------

func TestWriteReadFile(t *testing.T) {
	a, _, cleanup := newTestAdapter(t)
	defer cleanup()
	ctx := context.Background()

	s, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "img"})

	// ReadFile goes through the execd DownloadFile path.
	// First seed a file via UploadFile, then read it back.
	content := "hello, opensandbox"
	err := a.UploadFile(ctx, s.SandboxID, &adapter.FileUploadRequest{
		Path:   "/tmp/hello.txt",
		Reader: io.NopCloser(bytes.NewBufferString(content)),
	})
	if err != nil {
		t.Fatalf("UploadFile error: %v", err)
	}

	fc, err := a.ReadFile(ctx, s.SandboxID, "/tmp/hello.txt")
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(fc.Content) != content {
		t.Errorf("ReadFile content = %q, want %q", string(fc.Content), content)
	}
	if fc.Size != int64(len(content)) {
		t.Errorf("Size = %d, want %d", fc.Size, len(content))
	}
}

func TestUploadDownloadFile(t *testing.T) {
	a, _, cleanup := newTestAdapter(t)
	defer cleanup()
	ctx := context.Background()

	s, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "img"})

	body := "file body content"
	if err := a.UploadFile(ctx, s.SandboxID, &adapter.FileUploadRequest{
		Path:   "/data/file.txt",
		Reader: io.NopCloser(bytes.NewBufferString(body)),
	}); err != nil {
		t.Fatalf("UploadFile error: %v", err)
	}

	rc, err := a.DownloadFile(ctx, s.SandboxID, "/data/file.txt")
	if err != nil {
		t.Fatalf("DownloadFile error: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != body {
		t.Errorf("DownloadFile body = %q, want %q", string(got), body)
	}
}

func TestListFiles(t *testing.T) {
	a, _, cleanup := newTestAdapter(t)
	defer cleanup()
	ctx := context.Background()

	s, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "img"})

	_ = a.UploadFile(ctx, s.SandboxID, &adapter.FileUploadRequest{Path: "/dir/a.txt", Reader: io.NopCloser(bytes.NewBufferString("aaa"))})
	_ = a.UploadFile(ctx, s.SandboxID, &adapter.FileUploadRequest{Path: "/dir/b.txt", Reader: io.NopCloser(bytes.NewBufferString("bbb"))})

	entries, err := a.ListFiles(ctx, s.SandboxID, "/dir")
	if err != nil {
		t.Fatalf("ListFiles error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	for _, e := range entries {
		if e.IsDir {
			t.Errorf("file %q reported as dir", e.Path)
		}
	}
}

func TestMakeDirAndRemoveFile(t *testing.T) {
	a, srv, cleanup := newTestAdapter(t)
	defer cleanup()
	ctx := context.Background()

	s, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "img"})

	if err := a.MakeDir(ctx, s.SandboxID, "/mydir"); err != nil {
		t.Fatalf("MakeDir error: %v", err)
	}
	// The fake represents a directory as a sentinel entry with nil contents.
	srv.mu.Lock()
	_, ok := srv.files[s.SandboxID]["/mydir"]
	srv.mu.Unlock()
	if !ok {
		t.Error("MakeDir did not create directory sentinel entry")
	}

	_ = a.UploadFile(ctx, s.SandboxID, &adapter.FileUploadRequest{Path: "/mydir/f.txt", Reader: io.NopCloser(bytes.NewBufferString("x"))})

	if err := a.RemoveFile(ctx, s.SandboxID, "/mydir/f.txt"); err != nil {
		t.Fatalf("RemoveFile error: %v", err)
	}
	srv.mu.Lock()
	_, stillThere := srv.files[s.SandboxID]["/mydir/f.txt"]
	srv.mu.Unlock()
	if stillThere {
		t.Error("RemoveFile did not delete the file")
	}
}

func TestMoveFile(t *testing.T) {
	a, srv, cleanup := newTestAdapter(t)
	defer cleanup()
	ctx := context.Background()

	s, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "img"})

	_ = a.UploadFile(ctx, s.SandboxID, &adapter.FileUploadRequest{Path: "/src.txt", Reader: io.NopCloser(bytes.NewBufferString("data"))})

	if err := a.MoveFile(ctx, s.SandboxID, "/src.txt", "/dst.txt"); err != nil {
		t.Fatalf("MoveFile error: %v", err)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if _, ok := srv.files[s.SandboxID]["/src.txt"]; ok {
		t.Error("src still exists after move")
	}
	if got, ok := srv.files[s.SandboxID]["/dst.txt"]; !ok {
		t.Error("dst not created by move")
	} else if string(got) != "data" {
		t.Errorf("dst content = %q, want %q", string(got), "data")
	}
}

// ---------------------------------------------------------------------------
// Templates / unsupported operations (smoke)
// ---------------------------------------------------------------------------

func TestTemplatesReturnEmpty(t *testing.T) {
	a, _, cleanup := newTestAdapter(t)
	defer cleanup()
	ctx := context.Background()

	templates, err := a.ListTemplates(ctx, adapter.ListOptions{})
	if err != nil {
		t.Fatalf("ListTemplates error: %v", err)
	}
	if len(templates) != 0 {
		t.Errorf("ListTemplates returned %d items, want 0", len(templates))
	}

	tpl, err := a.GetTemplate(ctx, "some-image")
	if err != nil {
		t.Fatalf("GetTemplate error: %v", err)
	}
	if tpl.TemplateID != "some-image" {
		t.Errorf("TemplateID = %q, want %q", tpl.TemplateID, "some-image")
	}
}

func TestUnsupportedOperations(t *testing.T) {
	a, _, cleanup := newTestAdapter(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := a.CreateTemplate(ctx, nil); err == nil {
		t.Error("CreateTemplate expected error")
	}
	if err := a.DeleteTemplate(ctx, "x"); err == nil {
		t.Error("DeleteTemplate expected error")
	}
	if _, err := a.TriggerBuild(ctx, "x", nil); err == nil {
		t.Error("TriggerBuild expected error")
	}
	if _, err := a.GetBuildStatus(ctx, "x", "y"); err == nil {
		t.Error("GetBuildStatus expected error")
	}
	if err := a.CreateAlias(ctx, "x", "y"); err == nil {
		t.Error("CreateAlias expected error")
	}
	if err := a.DeleteAlias(ctx, "x", "y"); err == nil {
		t.Error("DeleteAlias expected error")
	}
	if _, err := a.CreateWarmPool(ctx, nil); err == nil {
		t.Error("CreateWarmPool expected error")
	}
	if _, err := a.GetWarmPool(ctx, "x"); err == nil {
		t.Error("GetWarmPool expected error")
	}
	if err := a.DeleteWarmPool(ctx, "x"); err == nil {
		t.Error("DeleteWarmPool expected error")
	}
	if err := a.UpdateWarmPoolSize(ctx, "x", 1); err == nil {
		t.Error("UpdateWarmPoolSize expected error")
	}
	if err := a.SendStdin(ctx, "x", "y", "z"); err == nil {
		t.Error("SendStdin expected error")
	}
	if _, err := a.CreateSnapshot(ctx, "x", nil); err == nil {
		t.Error("CreateSnapshot expected error")
	}
	if _, err := a.GetPortURL(ctx, "x", 80); err == nil {
		t.Error("GetPortURL expected error")
	}
	if _, err := a.GetAccessToken(ctx, "x"); err == nil {
		t.Error("GetAccessToken expected error")
	}
	if err := a.SetEnvs(ctx, "x", nil); err == nil {
		t.Error("SetEnvs expected error")
	}
	if _, err := a.CreateTag(ctx, "x", nil); err == nil {
		t.Error("CreateTag expected error")
	}
	if err := a.DeleteTag(ctx, "x", "y"); err == nil {
		t.Error("DeleteTag expected error")
	}

	// Operations that return empty lists should not error.
	if pools, err := a.ListWarmPools(ctx); err != nil || pools == nil {
		t.Errorf("ListWarmPools = %v, %v; want nil error", pools, err)
	}
	if snaps, err := a.ListSnapshots(ctx, "x"); err != nil || snaps == nil {
		t.Errorf("ListSnapshots = %v, %v; want nil error", snaps, err)
	}
	if ports, err := a.ListPorts(ctx, "x"); err != nil || ports == nil {
		t.Errorf("ListPorts = %v, %v; want nil error", ports, err)
	}
	if logs, err := a.GetLogs(ctx, "x"); err != nil || logs == nil {
		t.Errorf("GetLogs = %v, %v; want nil error", logs, err)
	}
	if tags, err := a.ListTags(ctx, "x"); err != nil || tags == nil {
		t.Errorf("ListTags = %v, %v; want nil error", tags, err)
	}
}

// ---------------------------------------------------------------------------
// Per-sandbox execd client caching
// ---------------------------------------------------------------------------

func TestExecdClientCache(t *testing.T) {
	a, _, cleanup := newTestAdapter(t)
	defer cleanup()
	ctx := context.Background()

	s, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "img"})

	c1, err := a.getOrCreateExecdClient(ctx, s.SandboxID)
	if err != nil {
		t.Fatalf("getOrCreateExecdClient error: %v", err)
	}
	c2, err := a.getOrCreateExecdClient(ctx, s.SandboxID)
	if err != nil {
		t.Fatalf("getOrCreateExecdClient error: %v", err)
	}
	if c1 != c2 {
		t.Error("expected cached ExecdClient to be reused on second call")
	}

	// Different sandbox -> different client.
	s2, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "img"})
	c3, err := a.getOrCreateExecdClient(ctx, s2.SandboxID)
	if err != nil {
		t.Fatalf("getOrCreateExecdClient error: %v", err)
	}
	if c1 == c3 {
		t.Error("different sandboxes should have different ExecdClients")
	}
}
