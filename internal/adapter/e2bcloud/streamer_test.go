package e2bcloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/streaming"
)

// collectStream is a test helper that implements adapter.CodeStream by
// collecting all sent messages into a slice.
type collectStream struct {
	mu       sync.Mutex
	messages []*adapter.StreamMessage
	closed   bool
}

func (s *collectStream) Send(msg *adapter.StreamMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, msg)
	return nil
}

func (s *collectStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *collectStream) Messages() []*adapter.StreamMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*adapter.StreamMessage, len(s.messages))
	copy(out, s.messages)
	return out
}

func (s *collectStream) IsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// mockEnvdWSServer creates a test WebSocket server that simulates the E2B
// Cloud envd daemon. It responds to code/exec frames with the given sequence
// of response frames.
func mockEnvdWSServer(t *testing.T, responseFrames []envdWSFrame) *httptest.Server {
	t.Helper()

	upgrader := websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool { return true },
	}

	mux := http.NewServeMux()

	// Access token endpoint.
	mux.HandleFunc("/sandboxes/test-sbx-1/access-token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accessToken":"test-token","expiresAt":"2099-01-01T00:00:00Z"}`))
	})

	// WebSocket endpoint.
	mux.HandleFunc("/sandboxes/test-sbx-1/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Read the code/exec frame (we don't need to validate it for tests).
		_, _, err = conn.ReadMessage()
		if err != nil {
			return
		}

		// Send response frames.
		for _, frame := range responseFrames {
			data, err := json.Marshal(frame)
			if err != nil {
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}

		// Send close frame.
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done"),
			time.Now().Add(5*time.Second),
		)
	})

	return httptest.NewServer(mux)
}

func TestCodeStreamer_Stream_Success(t *testing.T) {
	// Build response frames.
	stdoutData, _ := json.Marshal(envdStdoutPayload{Content: "hello world\n", Timestamp: "2026-01-01T00:00:00Z"})
	stderrData, _ := json.Marshal(envdStdoutPayload{Content: "warning!\n"})
	resultData, _ := json.Marshal(envdResultPayload{ExitCode: 0, Duration: 0.5})

	frames := []envdWSFrame{
		{Type: streaming.FrameStdout, Data: stdoutData},
		{Type: streaming.FrameStderr, Data: stderrData},
		{Type: streaming.FrameResult, Data: resultData},
	}

	ts := mockEnvdWSServer(t, frames)
	defer ts.Close()

	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key"})
	streamer := NewCodeStreamer(client)

	stream := &collectStream{}
	err := streamer.Stream(context.Background(), "test-sbx-1", &adapter.CodeExecutionRequest{
		Code:     "print('hello')",
		Language: "python",
	}, stream)

	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	msgs := stream.Messages()
	if len(msgs) < 3 {
		t.Fatalf("expected at least 3 messages, got %d", len(msgs))
	}

	// Check stdout.
	if msgs[0].Type != "stdout" {
		t.Errorf("msg[0].Type = %q, want 'stdout'", msgs[0].Type)
	}
	if msgs[0].Data != "hello world\n" {
		t.Errorf("msg[0].Data = %q, want 'hello world\\n'", msgs[0].Data)
	}

	// Check stderr.
	if msgs[1].Type != "stderr" {
		t.Errorf("msg[1].Type = %q, want 'stderr'", msgs[1].Type)
	}

	// Check result.
	if msgs[2].Type != "result" {
		t.Errorf("msg[2].Type = %q, want 'result'", msgs[2].Type)
	}
	resultMap, ok := msgs[2].Data.(map[string]interface{})
	if !ok {
		t.Fatalf("msg[2].Data is not a map, got %T", msgs[2].Data)
	}
	if ec, ok := resultMap["exitCode"].(int); !ok || ec != 0 {
		t.Errorf("exitCode = %v, want 0", resultMap["exitCode"])
	}

	// Verify stream was closed.
	if !stream.IsClosed() {
		t.Error("stream should be closed after Stream() returns")
	}
}

func TestCodeStreamer_Stream_Error(t *testing.T) {
	errorData, _ := json.Marshal(envdErrorPayload{Code: "runtime_error", Message: "undefined variable"})

	frames := []envdWSFrame{
		{Type: streaming.FrameError, Data: errorData},
	}

	ts := mockEnvdWSServer(t, frames)
	defer ts.Close()

	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key"})
	streamer := NewCodeStreamer(client)

	stream := &collectStream{}
	err := streamer.Stream(context.Background(), "test-sbx-1", &adapter.CodeExecutionRequest{
		Code: "print(undefined_var)",
	}, stream)

	if err == nil {
		t.Fatal("expected error from Stream()")
	}

	msgs := stream.Messages()
	if len(msgs) < 1 {
		t.Fatal("expected at least 1 message")
	}

	// Should have an error message.
	found := false
	for _, msg := range msgs {
		if msg.Type == "error" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected an error message in the stream")
	}
}

func TestCodeStreamer_Stream_ContextCancel(t *testing.T) {
	// Server that sends one frame then keeps connection open.
	upgrader := websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool { return true },
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/sandboxes/test-sbx-1/access-token", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"accessToken":"test-token","expiresAt":"2099-01-01T00:00:00Z"}`))
	})
	mux.HandleFunc("/sandboxes/test-sbx-1/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Read code/exec frame.
		_, _, _ = conn.ReadMessage()

		// Send one stdout frame.
		stdoutData, _ := json.Marshal(envdStdoutPayload{Content: "partial output"})
		data, _ := json.Marshal(envdWSFrame{Type: streaming.FrameStdout, Data: stdoutData})
		_ = conn.WriteMessage(websocket.TextMessage, data)

		// Keep reading until the client disconnects.
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key"})
	streamer := NewCodeStreamer(client)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	stream := &collectStream{}
	start := time.Now()
	err := streamer.Stream(ctx, "test-sbx-1", &adapter.CodeExecutionRequest{
		Code: "import time; time.sleep(60)",
	}, stream)
	elapsed := time.Since(start)

	// Should have been canceled or connection should have been interrupted.
	if err == nil {
		t.Fatal("expected error from canceled context")
	}
	// Accept either context cancellation or connection reset as valid outcomes.
	errMsg := err.Error()
	if !strings.Contains(errMsg, "cancel") && !strings.Contains(errMsg, "connection reset") && !strings.Contains(errMsg, "context") && !strings.Contains(errMsg, "read:") {
		t.Errorf("expected cancellation or connection error, got: %v", err)
	}
	// Should complete within 2 seconds (not wait for 60s).
	if elapsed > 2*time.Second {
		t.Errorf("took too long to cancel: %v", elapsed)
	}
}

func TestCodeStreamer_Stream_ConnectionRefused(t *testing.T) {
	// Server that only handles access-token but not WebSocket.
	mux := http.NewServeMux()
	mux.HandleFunc("/sandboxes/test-sbx-1/access-token", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"accessToken":"test-token","expiresAt":"2099-01-01T00:00:00Z"}`))
	})
	// No /ws endpoint → dial will fail.

	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key"})
	streamer := NewCodeStreamer(client)

	stream := &collectStream{}
	err := streamer.Stream(context.Background(), "test-sbx-1", &adapter.CodeExecutionRequest{
		Code: "print('hello')",
	}, stream)

	if err == nil {
		t.Fatal("expected error for connection refused")
	}

	// Should have sent an error message to the stream.
	msgs := stream.Messages()
	found := false
	for _, msg := range msgs {
		if msg.Type == "error" {
			found = true
		}
	}
	if !found {
		t.Error("expected an error message in the stream")
	}

	// Stream should be closed.
	if !stream.IsClosed() {
		t.Error("stream should be closed")
	}
}

func TestAdapter_ExecuteCodeStream_Fallback(t *testing.T) {
	// When WebSocket is unavailable, should fallback to sync ExecuteCode.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access-token") {
			_, _ = w.Write([]byte(`{"accessToken":"test-token"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/code") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"stdout":"sync output\n","exitCode":0}`))
			return
		}
		// Return 404 for WebSocket endpoint.
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key"})
	a := NewAdapterWithClient("e2b-cloud", client)

	stream := &collectStream{}
	err := a.ExecuteCodeStream(context.Background(), "test-sbx-1", &adapter.CodeExecutionRequest{
		Code:     "print('hello')",
		Language: "python",
	}, stream)

	if err != nil {
		t.Fatalf("ExecuteCodeStream() error: %v", err)
	}

	msgs := stream.Messages()
	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 messages (stdout + result), got %d", len(msgs))
	}

	// Should have stdout from sync execution.
	foundStdout := false
	foundResult := false
	for _, msg := range msgs {
		switch msg.Type {
		case "stdout":
			foundStdout = true
			if msg.Data != "sync output\n" {
				t.Errorf("stdout data = %q, want 'sync output\\n'", msg.Data)
			}
		case "result":
			foundResult = true
		}
	}
	if !foundStdout {
		t.Error("missing stdout message from sync fallback")
	}
	if !foundResult {
		t.Error("missing result message from sync fallback")
	}
}

func TestIsWebSocketNotAvailable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"dial error", &testError{"dial tcp: connection refused"}, true},
		{"connection refused", &testError{"connection refused"}, true},
		{"bad handshake", &testError{"websocket: bad handshake"}, true},
		{"no such host", &testError{"no such host"}, true},
		{"other error", &testError{"some other error"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isWebSocketNotAvailable(tt.err)
			if got != tt.want {
				t.Errorf("isWebSocketNotAvailable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
