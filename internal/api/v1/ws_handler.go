// Package v1 — ws_handler.go implements a WebSocket handler for streaming
// code execution, compatible with the E2B SDK WebSocket protocol.
//
// Protocol flow:
//  1. Client upgrades to WebSocket at /sandboxes/{sandboxID}/ws
//  2. Client sends: {"type":"code/exec","data":{"code":"...","language":"..."}}
//  3. Server streams: {"type":"stdout","data":"..."}, {"type":"stderr","data":"..."}
//  4. Server sends: {"type":"result","data":{"exitCode":0,"duration":1.23}}
//  5. Connection stays open for subsequent executions or terminal sessions
package v1

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/api/dto"
	"github.com/e2bgateway/e2bgateway/internal/routing"
	"github.com/e2bgateway/e2bgateway/internal/streaming"
)

// wsUpgrader upgrades HTTP connections to WebSocket for streaming execution.
var wsUpgrader = websocket.Upgrader{
	CheckOrigin:      func(_ *http.Request) bool { return true },
	ReadBufferSize:   4096,
	WriteBufferSize:  4096,
	HandshakeTimeout: 10 * time.Second,
}

// ExecuteCodeStreamHandler handles WebSocket connections for streaming code
// execution. Compatible with the E2B SDK WebSocket protocol.
func ExecuteCodeStreamHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")
		if sandboxID == "" {
			writeError(w, http.StatusBadRequest, "missing sandboxID")
			return
		}

		a, err := resolveAdapter(w, r, registry, router, sandboxID)
		if err != nil {
			return // Error already written.
		}

		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		executionID := fmt.Sprintf("exec-%s-%d", sandboxID, time.Now().UnixNano())
		bridge := newWSCodeStream(conn, executionID)
		defer func() { _ = bridge.close() }()

		readLoop(r, conn, a, sandboxID, bridge)
	}
}

// resolveAdapter selects and validates the backend adapter for the request.
func resolveAdapter(w http.ResponseWriter, r *http.Request, registry *adapter.Registry, router *routing.Router, sandboxID string) (adapter.SandboxAdapter, error) {
	token := r.Header.Get("X-Access-Token")
	if token == "" {
		token = r.URL.Query().Get("access_token")
	}

	backendName, err := router.SelectBackend(r.Context(), &routing.RoutingRequest{})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return nil, err
	}
	a, ok := registry.Get(backendName)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "backend not found")
		return nil, fmt.Errorf("backend not found")
	}

	if token != "" {
		valid, verr := a.ValidateAccessToken(r.Context(), sandboxID, token)
		if verr != nil || !valid {
			writeError(w, http.StatusUnauthorized, "invalid access token")
			return nil, fmt.Errorf("invalid access token")
		}
	}
	return a, nil
}

// readLoop reads client WebSocket frames and dispatches them.
func readLoop(r *http.Request, conn *websocket.Conn, a adapter.SandboxAdapter, sandboxID string, bridge *wsCodeStream) {
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var frame streaming.Frame
		if err := json.Unmarshal(msg, &frame); err != nil {
			continue
		}

		switch frame.Type {
		case streaming.FrameCodeExec:
			handleCodeExec(r, a, sandboxID, bridge, frame)
		case streaming.FrameCancel:
			bridge.cancel()
		case streaming.FrameKeepAlive:
			bridge.sendKeepAlive()
		}
	}
}

// handleCodeExec processes a code/exec frame by invoking the adapter's
// ExecuteCodeStream method.
func handleCodeExec(r *http.Request, a adapter.SandboxAdapter, sandboxID string, bridge *wsCodeStream, frame streaming.Frame) {
	execData := parseCodeExecData(frame.Data)
	req := &adapter.CodeExecutionRequest{Code: execData.Code, Language: execData.Language}

	bridge.reset()
	if err := a.ExecuteCodeStream(r.Context(), sandboxID, req, bridge); err != nil {
		bridge.sendError("execution_error", err.Error())
	}
	bridge.sendKeepAlive()
}

// parseCodeExecData extracts code and language from a frame's data field.
func parseCodeExecData(data interface{}) streaming.CodeExecData {
	var result streaming.CodeExecData
	if fd, ok := data.(map[string]interface{}); ok {
		if code, ok := fd["code"].(string); ok {
			result.Code = code
		}
		if lang, ok := fd["language"].(string); ok {
			result.Language = lang
		}
		return result
	}
	if raw, ok := data.(json.RawMessage); ok {
		_ = json.Unmarshal(raw, &result)
	}
	return result
}

// wsCodeStream bridges adapter.CodeStream to a WebSocket connection.
// It converts StreamMessage objects to E2B-standardized WebSocket frames
// using streaming.Normalizer.
type wsCodeStream struct {
	conn        *websocket.Conn
	executionID string
	norm        *streaming.Normalizer
	mu          sync.Mutex
	canceled    bool
	closed      bool
}

func newWSCodeStream(conn *websocket.Conn, executionID string) *wsCodeStream {
	return &wsCodeStream{
		conn:        conn,
		executionID: executionID,
		norm:        streaming.NewNormalizer(executionID),
	}
}

func (w *wsCodeStream) reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.canceled = false
}

func (w *wsCodeStream) cancel() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.canceled = true
}

// Send converts a StreamMessage to a standardized WebSocket frame and writes
// it to the connection. Uses streaming.Normalizer for frame standardization.
func (w *wsCodeStream) Send(msg *adapter.StreamMessage) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed || w.canceled {
		return nil
	}

	frame := w.buildFrame(msg)
	data, err := frame.Marshal()
	if err != nil {
		return fmt.Errorf("marshaling frame: %w", err)
	}

	_ = w.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return w.conn.WriteMessage(websocket.TextMessage, data)
}

// buildFrame converts a StreamMessage to a streaming.Frame using the Normalizer.
func (w *wsCodeStream) buildFrame(msg *adapter.StreamMessage) *streaming.Frame {
	switch msg.Type {
	case "stdout":
		return w.norm.NormalizeStdout(extractStringData(msg.Data))
	case "stderr":
		return w.norm.NormalizeStderr(extractStringData(msg.Data))
	case "result":
		exitCode, duration := parseResultData(msg.Data)
		return w.norm.NormalizeExitCode(exitCode, duration)
	case "error":
		code, message := parseErrorData(msg.Data)
		return w.norm.NormalizeError(code, message)
	default:
		return &streaming.Frame{Type: msg.Type, Data: msg.Data}
	}
}

func (w *wsCodeStream) Close() error { return w.close() }

func (w *wsCodeStream) close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return w.conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "execution complete"),
		time.Now().Add(5*time.Second),
	)
}

func (w *wsCodeStream) sendError(code, message string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	frame := w.norm.NormalizeError(code, message)
	data, _ := frame.Marshal()
	_ = w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = w.conn.WriteMessage(websocket.TextMessage, data)
}

func (w *wsCodeStream) sendKeepAlive() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	frame := streaming.NewKeepAliveFrame()
	data, _ := frame.Marshal()
	_ = w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = w.conn.WriteMessage(websocket.TextMessage, data)
}

// --- Data parsing helpers ---

func extractStringData(data interface{}) string {
	switch v := data.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		b, err := json.Marshal(data)
		if err != nil {
			return fmt.Sprintf("%v", data)
		}
		return string(b)
	}
}

func parseResultData(data interface{}) (int, float64) {
	exitCode, duration := 0, 0.0
	if m, ok := data.(map[string]interface{}); ok {
		if ec, ok := m["exitCode"].(int); ok {
			exitCode = ec
		}
		if d, ok := m["duration"].(float64); ok {
			duration = d
		}
	}
	return exitCode, duration
}

func parseErrorData(data interface{}) (string, string) {
	code, message := "unknown", ""
	switch d := data.(type) {
	case map[string]interface{}:
		if c, ok := d["code"].(string); ok {
			code = c
		}
		if m, ok := d["message"].(string); ok {
			message = m
		}
	case string:
		message = d
	}
	return code, message
}

// Ensure WSMessage is referenced to avoid import errors.
var _ = dto.WSMessage{}
