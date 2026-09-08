package v1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/routing"
)

// stubAdapter is a minimal SandboxAdapter for testing the WebSocket handler.
// Only the methods used by ExecuteCodeStreamHandler are implemented.
type stubAdapter struct {
	name    string
	streamF func(ctx context.Context, sandboxID string, req *adapter.CodeExecutionRequest, stream adapter.CodeStream) error
}

func (s *stubAdapter) Name() string { return s.name }

func (s *stubAdapter) ExecuteCodeStream(ctx context.Context, sandboxID string, req *adapter.CodeExecutionRequest, stream adapter.CodeStream) error {
	if s.streamF != nil {
		return s.streamF(ctx, sandboxID, req, stream)
	}
	// Default: send stdout + result.
	_ = stream.Send(&adapter.StreamMessage{Type: "stdout", Data: "hello from stub"})
	_ = stream.Send(&adapter.StreamMessage{Type: "result", Data: map[string]interface{}{"exitCode": 0}})
	return stream.Close()
}

func (s *stubAdapter) ValidateAccessToken(_ context.Context, _, _ string) (bool, error) {
	return true, nil
}

// Implement the rest of the SandboxAdapter interface with panics (unused in tests).
func (s *stubAdapter) HealthCheck(_ context.Context) error { return nil }
func (s *stubAdapter) CreateSandbox(_ context.Context, _ *adapter.CreateSandboxRequest) (*adapter.Sandbox, error) {
	panic("not implemented")
}
func (s *stubAdapter) ListSandboxes(_ context.Context, _ adapter.ListOptions) ([]*adapter.Sandbox, error) {
	panic("not implemented")
}
func (s *stubAdapter) GetSandbox(_ context.Context, _ string) (*adapter.Sandbox, error) {
	panic("not implemented")
}
func (s *stubAdapter) KillSandbox(_ context.Context, _ string) error  { panic("not implemented") }
func (s *stubAdapter) PauseSandbox(_ context.Context, _ string) error { panic("not implemented") }
func (s *stubAdapter) ResumeSandbox(_ context.Context, _ string) (*adapter.Sandbox, error) {
	panic("not implemented")
}
func (s *stubAdapter) SetTimeout(_ context.Context, _ string, _ time.Duration) error {
	panic("not implemented")
}
func (s *stubAdapter) ExecuteCode(_ context.Context, _ string, _ *adapter.CodeExecutionRequest) (*adapter.CodeExecutionResult, error) {
	panic("not implemented")
}
func (s *stubAdapter) RunCommand(_ context.Context, _ string, _ *adapter.CommandRequest) (*adapter.CommandResult, error) {
	panic("not implemented")
}
func (s *stubAdapter) WriteFile(_ context.Context, _ string, _ *adapter.FileWriteRequest) error {
	panic("not implemented")
}
func (s *stubAdapter) ReadFile(_ context.Context, _ string, _ string) (*adapter.FileContent, error) {
	panic("not implemented")
}
func (s *stubAdapter) UploadFile(_ context.Context, _ string, _ *adapter.FileUploadRequest) error {
	panic("not implemented")
}
func (s *stubAdapter) DownloadFile(_ context.Context, _ string, _ string) (io.ReadCloser, error) {
	panic("not implemented")
}
func (s *stubAdapter) ListFiles(_ context.Context, _ string, _ string) ([]adapter.FileInfo, error) {
	panic("not implemented")
}
func (s *stubAdapter) MakeDir(_ context.Context, _ string, _ string) error { panic("not implemented") }
func (s *stubAdapter) RemoveFile(_ context.Context, _ string, _ string) error {
	panic("not implemented")
}
func (s *stubAdapter) ListTemplates(_ context.Context, _ adapter.ListOptions) ([]*adapter.Template, error) {
	panic("not implemented")
}
func (s *stubAdapter) GetTemplate(_ context.Context, _ string) (*adapter.Template, error) {
	panic("not implemented")
}
func (s *stubAdapter) CreateTemplate(_ context.Context, _ *adapter.CreateTemplateRequest) (*adapter.TemplateBuild, error) {
	panic("not implemented")
}
func (s *stubAdapter) DeleteTemplate(_ context.Context, _ string) error { panic("not implemented") }
func (s *stubAdapter) TriggerBuild(_ context.Context, _ string, _ *adapter.BuildRequest) (*adapter.TemplateBuild, error) {
	panic("not implemented")
}
func (s *stubAdapter) GetBuildStatus(_ context.Context, _, _ string) (*adapter.BuildStatus, error) {
	panic("not implemented")
}
func (s *stubAdapter) CreateAlias(_ context.Context, _, _ string) error { panic("not implemented") }
func (s *stubAdapter) DeleteAlias(_ context.Context, _, _ string) error { panic("not implemented") }
func (s *stubAdapter) ListWarmPools(_ context.Context) ([]*adapter.WarmPool, error) {
	panic("not implemented")
}
func (s *stubAdapter) CreateWarmPool(_ context.Context, _ *adapter.WarmPoolCreateRequest) (*adapter.WarmPool, error) {
	panic("not implemented")
}
func (s *stubAdapter) GetWarmPool(_ context.Context, _ string) (*adapter.WarmPool, error) {
	panic("not implemented")
}
func (s *stubAdapter) DeleteWarmPool(_ context.Context, _ string) error { panic("not implemented") }
func (s *stubAdapter) UpdateWarmPoolSize(_ context.Context, _ string, _ int) error {
	panic("not implemented")
}
func (s *stubAdapter) ListProcesses(_ context.Context, _ string) ([]*adapter.ProcessInfo, error) {
	panic("not implemented")
}
func (s *stubAdapter) KillProcess(_ context.Context, _, _ string) error { panic("not implemented") }
func (s *stubAdapter) SendStdin(_ context.Context, _, _ string, _ string) error {
	panic("not implemented")
}
func (s *stubAdapter) CreateSnapshot(_ context.Context, _ string, _ *adapter.SnapshotRequest) (*adapter.Snapshot, error) {
	panic("not implemented")
}
func (s *stubAdapter) ListSnapshots(_ context.Context, _ string) ([]*adapter.Snapshot, error) {
	panic("not implemented")
}
func (s *stubAdapter) ListPorts(_ context.Context, _ string) ([]*adapter.PortInfo, error) {
	panic("not implemented")
}
func (s *stubAdapter) GetPortURL(_ context.Context, _ string, _ int) (string, error) {
	panic("not implemented")
}
func (s *stubAdapter) GetAccessToken(_ context.Context, _ string) (*adapter.AccessToken, error) {
	panic("not implemented")
}
func (s *stubAdapter) SetEnvs(_ context.Context, _ string, _ map[string]string) error {
	panic("not implemented")
}
func (s *stubAdapter) GetLogs(_ context.Context, _ string) ([]*adapter.LogEntry, error) {
	panic("not implemented")
}
func (s *stubAdapter) MoveFile(_ context.Context, _, _ string, _ string) error {
	panic("not implemented")
}
func (s *stubAdapter) CreateTag(_ context.Context, _ string, _ *adapter.TagRequest) (*adapter.Tag, error) {
	panic("not implemented")
}
func (s *stubAdapter) ListTags(_ context.Context, _ string) ([]*adapter.Tag, error) {
	panic("not implemented")
}
func (s *stubAdapter) DeleteTag(_ context.Context, _, _ string) error { panic("not implemented") }
func (s *stubAdapter) GetEnvdEndpoint(_ context.Context, _ string) (string, string, error) {
	panic("not implemented")
}

// newTestRouter creates a chi router with the WebSocket handler mounted.
func newTestRouter(reg *adapter.Registry) chi.Router {
	r := chi.NewRouter()
	routeMgr := routing.NewRouter(config.RoutingConfig{Strategy: "static"}, reg)
	r.Get("/sandboxes/{sandboxID}/ws", ExecuteCodeStreamHandler(reg, routeMgr))
	return r
}

func TestExecuteCodeStreamHandler_Upgrade(t *testing.T) {
	reg := adapter.NewRegistry()
	a := &stubAdapter{name: "test"}
	_ = reg.Register(a)

	router := newTestRouter(reg)
	srv := httptest.NewServer(router)
	defer srv.Close()

	// Connect via WebSocket.
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/sandboxes/test-sbx/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WebSocket dial error: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Send a code/exec frame.
	execFrame := map[string]interface{}{
		"type": "code/exec",
		"data": map[string]interface{}{
			"code":     "print('hello')",
			"language": "python",
		},
	}
	data, _ := json.Marshal(execFrame)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("WriteMessage error: %v", err)
	}

	// Read response frames.
	var receivedFrames []map[string]interface{}
	for i := 0; i < 3; i++ { // stdout + result + keepAlive
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var frame map[string]interface{}
		if err := json.Unmarshal(msg, &frame); err != nil {
			continue
		}
		receivedFrames = append(receivedFrames, frame)
	}

	if len(receivedFrames) < 2 {
		t.Fatalf("expected at least 2 frames (stdout + result), got %d", len(receivedFrames))
	}

	// First frame should be stdout.
	if ft, ok := receivedFrames[0]["type"].(string); !ok || ft != "stdout" {
		t.Errorf("frame[0].type = %v, want 'stdout'", receivedFrames[0]["type"])
	}

	// Second frame should be result.
	if ft, ok := receivedFrames[1]["type"].(string); !ok || ft != "result" {
		t.Errorf("frame[1].type = %v, want 'result'", receivedFrames[1]["type"])
	}
}

func TestExecuteCodeStreamHandler_MissingSandboxID(t *testing.T) {
	reg := adapter.NewRegistry()
	a := &stubAdapter{name: "test"}
	_ = reg.Register(a)

	router := newTestRouter(reg)

	// Request without sandboxID in URL — chi won't match the route.
	req := httptest.NewRequest(http.MethodGet, "/sandboxes//ws", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// chi should return 404 for unmatched routes.
	if w.Code != http.StatusNotFound && w.Code != http.StatusBadRequest {
		t.Errorf("expected 404 or 400, got %d", w.Code)
	}
}

func TestWsCodeStream_SendStdout(t *testing.T) {
	// Test the wsCodeStream bridge directly using a pipe-based WebSocket pair.
	// We use an HTTP server + WS upgrade, then create a bridge on the client
	// side and verify frames are written correctly.
	receivedCh := make(chan []byte, 10)
	doneCh := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Signal that the test can start.
		close(doneCh)

		// Read frames and forward to channel.
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			receivedCh <- msg
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Wait for the server to be ready.
	<-doneCh
	time.Sleep(50 * time.Millisecond) // Brief settle time.

	bridge := newWSCodeStream(conn, "test-exec-1")

	// Send a stdout message.
	err = bridge.Send(&adapter.StreamMessage{Type: "stdout", Data: "hello world"})
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}

	// Read the frame from the server side.
	select {
	case msg := <-receivedCh:
		var frame map[string]interface{}
		if err := json.Unmarshal(msg, &frame); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}

		if ft, ok := frame["type"].(string); !ok || ft != "stdout" {
			t.Errorf("frame type = %v, want 'stdout'", frame["type"])
		}

		// Data should contain the normalized stdout data.
		data, ok := frame["data"].(map[string]interface{})
		if !ok {
			t.Fatalf("frame data is not a map, got %T", frame["data"])
		}
		if content, ok := data["content"].(string); !ok || content != "hello world" {
			t.Errorf("data.content = %v, want 'hello world'", data["content"])
		}

	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for frame")
	}
}

func TestExtractStringData(t *testing.T) {
	tests := []struct {
		name string
		data interface{}
		want string
	}{
		{"string", "hello", "hello"},
		{"bytes", []byte("world"), "world"},
		{"int", 42, "42"},
		{"map", map[string]string{"key": "val"}, `{"key":"val"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractStringData(tt.data)
			if got != tt.want {
				t.Errorf("extractStringData(%v) = %q, want %q", tt.data, got, tt.want)
			}
		})
	}
}
