// Package e2bcloud — streamer.go implements WebSocket-based streaming code
// execution for the E2B Cloud adapter.
//
// The CodeStreamer connects to E2B Cloud's envd WebSocket endpoint, sends a
// code/exec frame, reads streaming response frames (stdout, stderr, result,
// error), and converts them into adapter.CodeStream messages. Frame output is
// standardized using streaming.Normalizer to ensure E2B SDK protocol
// compatibility.
package e2bcloud

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/streaming"
)

// CodeStreamer connects to E2B Cloud's envd WebSocket for streaming code
// execution. It uses the streaming.Normalizer to standardize output frames
// into the E2B SDK-compatible format.
type CodeStreamer struct {
	client *Client
	dialer *websocket.Dialer
}

// NewCodeStreamer creates a CodeStreamer backed by the given E2B Cloud client.
func NewCodeStreamer(client *Client) *CodeStreamer {
	return &CodeStreamer{
		client: client,
		dialer: &websocket.Dialer{
			HandshakeTimeout: 10 * time.Second,
			ReadBufferSize:   4096,
			WriteBufferSize:  4096,
		},
	}
}

// envdWSFrame represents a WebSocket frame exchanged with the E2B Cloud envd
// daemon. The wire format matches the E2B SDK protocol:
//
//	{"type":"stdout","data":{"content":"...","timestamp":"..."}}
//	{"type":"result","data":{"exitCode":0,"duration":1.23}}
type envdWSFrame struct {
	Type string          `json:"type"`
	ID   string          `json:"id,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// envdStdoutPayload is the data field for stdout/stderr frames.
type envdStdoutPayload struct {
	Content     string `json:"content"`
	Timestamp   string `json:"timestamp,omitempty"`
	ExecutionID string `json:"executionID,omitempty"`
}

// envdResultPayload is the data field for result frames.
type envdResultPayload struct {
	ExitCode    int     `json:"exitCode"`
	ExecutionID string  `json:"executionID,omitempty"`
	Duration    float64 `json:"duration,omitempty"`
}

// envdErrorPayload is the data field for error frames.
type envdErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// envdCodeExecPayload is the data field for code/exec request frames.
type envdCodeExecPayload struct {
	Code     string `json:"code"`
	Language string `json:"language,omitempty"`
}

// Stream connects to the E2B Cloud envd WebSocket, sends a code/exec frame,
// reads streaming response frames, and converts them to CodeStream messages.
//
// The method blocks until execution completes, the context is canceled, or an
// error occurs. On return, stream.Close() has been called.
//
// Frame normalization uses streaming.Normalizer so the output is compatible
// with the E2B SDK protocol.
func (s *CodeStreamer) Stream(ctx context.Context, sandboxID string, req *adapter.CodeExecutionRequest, stream adapter.CodeStream) error {
	defer func() { _ = stream.Close() }()

	conn, executionID, err := s.connect(ctx, sandboxID)
	if err != nil {
		code := "connection_error"
		if isAuthError(err) {
			code = "auth_error"
		}
		_ = stream.Send(&adapter.StreamMessage{Type: "error", Data: map[string]string{
			"code": code, "message": err.Error(),
		}})
		return err
	}
	defer func() { _ = conn.Close() }()

	norm := streaming.NewNormalizer(executionID)

	if err := s.sendExecFrame(conn, req, executionID); err != nil {
		_ = stream.Send(&adapter.StreamMessage{Type: "error", Data: map[string]string{
			"code": "write_error", "message": err.Error(),
		}})
		return err
	}

	// Set up context cancellation to close the WebSocket connection.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	return s.readLoop(ctx, conn, norm, stream)
}

// isAuthError returns true if the error is related to authentication.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "access token") || strings.Contains(msg, "auth")
}

// connect dials the envd WebSocket and returns the connection + execution ID.
func (s *CodeStreamer) connect(ctx context.Context, sandboxID string) (*websocket.Conn, string, error) {
	tokenResp, err := s.client.GetAccessToken(ctx, sandboxID)
	if err != nil {
		return nil, "", fmt.Errorf("getting access token: %w", err)
	}

	wsURL := s.buildEnvdWSURL(sandboxID, tokenResp.AccessToken)
	conn, _, err := s.dialer.DialContext(ctx, wsURL, http.Header{})
	if err != nil {
		return nil, "", fmt.Errorf("connecting to envd WebSocket: %w", err)
	}

	executionID := fmt.Sprintf("exec-%s-%d", sandboxID, time.Now().UnixNano())
	return conn, executionID, nil
}

// sendExecFrame sends the code/exec request frame over the WebSocket.
func (s *CodeStreamer) sendExecFrame(conn *websocket.Conn, req *adapter.CodeExecutionRequest, executionID string) error {
	execPayload := envdCodeExecPayload{Code: req.Code, Language: req.Language}
	execData, err := json.Marshal(execPayload)
	if err != nil {
		return fmt.Errorf("marshaling exec payload: %w", err)
	}
	execFrame := envdWSFrame{Type: streaming.FrameCodeExec, ID: executionID, Data: execData}
	frameBytes, err := json.Marshal(execFrame)
	if err != nil {
		return fmt.Errorf("marshaling exec frame: %w", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, frameBytes); err != nil {
		return fmt.Errorf("sending exec frame: %w", err)
	}
	return nil
}

// readLoop reads response frames from the WebSocket and forwards them to the
// CodeStream. It returns when a result or error frame is received, the
// connection is closed, or the context is canceled.
func (s *CodeStreamer) readLoop(ctx context.Context, conn *websocket.Conn, norm *streaming.Normalizer, stream adapter.CodeStream) error {
	startTime := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return s.sendCanceled(stream, err)
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			return s.handleReadError(ctx, err, stream)
		}

		done, err := s.handleFrame(msg, norm, stream, startTime)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

// handleFrame processes a single WebSocket frame. Returns (true, nil) when
// execution is complete (result frame), or (false, nil) to continue reading.
func (s *CodeStreamer) handleFrame(msg []byte, norm *streaming.Normalizer, stream adapter.CodeStream, startTime time.Time) (bool, error) {
	var frame envdWSFrame
	if err := json.Unmarshal(msg, &frame); err != nil {
		return false, nil // Skip malformed frames.
	}

	switch frame.Type {
	case streaming.FrameStdout:
		var payload envdStdoutPayload
		_ = json.Unmarshal(frame.Data, &payload)
		_ = norm.NormalizeStdout(payload.Content)
		if err := stream.Send(&adapter.StreamMessage{Type: "stdout", Data: payload.Content}); err != nil {
			return false, fmt.Errorf("sending stdout: %w", err)
		}
		return false, nil

	case streaming.FrameStderr:
		var payload envdStdoutPayload
		_ = json.Unmarshal(frame.Data, &payload)
		_ = norm.NormalizeStderr(payload.Content)
		if err := stream.Send(&adapter.StreamMessage{Type: "stderr", Data: payload.Content}); err != nil {
			return false, fmt.Errorf("sending stderr: %w", err)
		}
		return false, nil

	case streaming.FrameResult:
		var payload envdResultPayload
		_ = json.Unmarshal(frame.Data, &payload)
		duration := payload.Duration
		if duration == 0 {
			duration = time.Since(startTime).Seconds()
		}
		_ = norm.NormalizeExitCode(payload.ExitCode, duration)
		_ = stream.Send(&adapter.StreamMessage{
			Type: "result",
			Data: map[string]interface{}{
				"exitCode": payload.ExitCode, "duration": duration,
			},
		})
		return true, nil

	case streaming.FrameError:
		var payload envdErrorPayload
		_ = json.Unmarshal(frame.Data, &payload)
		_ = norm.NormalizeError(payload.Code, payload.Message)
		_ = stream.Send(&adapter.StreamMessage{
			Type: "error",
			Data: map[string]string{"code": payload.Code, "message": payload.Message},
		})
		return false, fmt.Errorf("envd execution error: %s: %s", payload.Code, payload.Message)

	default:
		return false, nil // Keep-alive or unknown — skip.
	}
}

// handleReadError processes a WebSocket read error, distinguishing between
// context cancellation, normal closure, and real errors.
func (s *CodeStreamer) handleReadError(ctx context.Context, err error, stream adapter.CodeStream) error {
	if ctx.Err() != nil {
		return s.sendCanceled(stream, ctx.Err())
	}
	if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		return nil
	}
	_ = stream.Send(&adapter.StreamMessage{Type: "error", Data: map[string]string{
		"code": "read_error", "message": fmt.Sprintf("reading from envd: %v", err),
	}})
	return fmt.Errorf("reading from envd WebSocket: %w", err)
}

// sendCanceled sends a canceled error message and returns the context error.
func (s *CodeStreamer) sendCanceled(stream adapter.CodeStream, ctxErr error) error {
	_ = stream.Send(&adapter.StreamMessage{Type: "error", Data: map[string]string{
		"code": "canceled", "message": "execution canceled",
	}})
	return ctxErr
}

// buildEnvdWSURL constructs the envd WebSocket URL for the given sandbox.
// The scheme is derived from the client's base URL: https → wss, http → ws.
func (s *CodeStreamer) buildEnvdWSURL(sandboxID, accessToken string) string {
	u, _ := url.Parse(s.client.baseURL)
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	u.Path = "/sandboxes/" + sandboxID + "/ws"
	q := u.Query()
	q.Set("access_token", accessToken)
	u.RawQuery = q.Encode()
	return u.String()
}
