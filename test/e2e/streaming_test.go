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

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/streaming"
)

const streamingTestSandboxID = "streaming-e2e-sandbox"

type streamWireFrame struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

type streamScript func(*websocket.Conn, streamWireFrame) error

// streamingTestServer runs a complete gateway in front of a scripted
// E2B Cloud-compatible WebSocket backend. This exercises both WebSocket hops:
// client -> gateway and gateway -> backend.
func streamingTestServer(t *testing.T, script streamScript) *httptest.Server {
	t.Helper()

	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/sandboxes", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	})
	mux.HandleFunc("/sandboxes/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/access-token"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"accessToken":"streaming-test-token","expiresAt":"2099-01-01T00:00:00Z"}`))
		case strings.HasSuffix(r.URL.Path, "/ws"):
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrading backend WebSocket: %v", err)
				return
			}
			defer func() { _ = conn.Close() }()

			var request streamWireFrame
			if err := conn.ReadJSON(&request); err != nil {
				t.Errorf("reading backend execution request: %v", err)
				return
			}
			if request.Type != streaming.FrameCodeExec {
				t.Errorf("backend request type = %q, want %q", request.Type, streaming.FrameCodeExec)
				return
			}
			if err := script(conn, request); err != nil {
				t.Errorf("running backend stream script: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	})

	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	return testServerWithConfig(t, &config.Config{
		Server: config.ServerConfig{HTTP: config.HTTPConfig{Address: "127.0.0.1:0"}},
		Backends: []config.BackendConfig{
			{
				Name:    "streaming-e2b-cloud",
				Type:    "e2b-cloud",
				Enabled: true,
				Config: map[string]any{
					"endpoint": upstream.URL,
					"apiKey":   "streaming-test-api-key",
				},
			},
		},
		Routing: config.RoutingConfig{
			DefaultBackend: "streaming-e2b-cloud",
			Strategy:       "static",
		},
	})
}

func dialStreamingGateway(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()

	dialer := websocketDialer()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/sandboxes/" + streamingTestSandboxID + "/ws"
	conn, resp, err := dialer.Dial(wsURL, http.Header{"X-API-Key": []string{"test-api-key"}})
	if resp != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	require.NoError(t, err, "dialing gateway WebSocket")
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func sendExecutionFrame(t *testing.T, conn *websocket.Conn, code string) {
	t.Helper()
	require.NoError(t, conn.WriteJSON(map[string]any{
		"type": streaming.FrameCodeExec,
		"data": map[string]any{"code": code, "language": "python"},
	}))
}

func readStreamFrame(t *testing.T, conn *websocket.Conn) streamWireFrame {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	var frame streamWireFrame
	require.NoError(t, conn.ReadJSON(&frame))
	return frame
}

func writeBackendFrame(conn *websocket.Conn, frameType string, data any) error {
	return conn.WriteJSON(map[string]any{"type": frameType, "data": data})
}

func TestE2E_StreamingMessageTypes(t *testing.T) {
	t.Run("stdout stderr result and keepalive", func(t *testing.T) {
		ts := streamingTestServer(t, func(conn *websocket.Conn, _ streamWireFrame) error {
			if err := writeBackendFrame(conn, streaming.FrameStdout, map[string]any{"content": "hello stdout\n"}); err != nil {
				return err
			}
			if err := writeBackendFrame(conn, streaming.FrameStderr, map[string]any{"content": "hello stderr\n"}); err != nil {
				return err
			}
			return writeBackendFrame(conn, streaming.FrameResult, map[string]any{"exitCode": 7, "duration": 0.25})
		})
		conn := dialStreamingGateway(t, ts)

		require.NoError(t, conn.WriteJSON(map[string]any{"type": streaming.FrameKeepAlive}))
		keepAlive := readStreamFrame(t, conn)
		assert.Equal(t, streaming.FrameKeepAlive, keepAlive.Type)

		sendExecutionFrame(t, conn, "message-types")
		stdout := readStreamFrame(t, conn)
		stderr := readStreamFrame(t, conn)
		result := readStreamFrame(t, conn)

		assert.Equal(t, streaming.FrameStdout, stdout.Type)
		assert.Equal(t, streaming.FrameStderr, stderr.Type)
		assert.Equal(t, streaming.FrameResult, result.Type)

		var stdoutData streaming.StdoutData
		require.NoError(t, json.Unmarshal(stdout.Data, &stdoutData))
		assert.Equal(t, "hello stdout\n", stdoutData.Content)
		assert.NotEmpty(t, stdoutData.ExecutionID)
		_, err := time.Parse(time.RFC3339Nano, stdoutData.Timestamp)
		require.NoError(t, err)

		var stderrData streaming.StdoutData
		require.NoError(t, json.Unmarshal(stderr.Data, &stderrData))
		assert.Equal(t, "hello stderr\n", stderrData.Content)
		assert.Equal(t, stdoutData.ExecutionID, stderrData.ExecutionID)

		var resultData streaming.ResultData
		require.NoError(t, json.Unmarshal(result.Data, &resultData))
		assert.Equal(t, 7, resultData.ExitCode)
		assert.Equal(t, 0.25, resultData.Duration)
		assert.Equal(t, stdoutData.ExecutionID, resultData.ExecutionID)
	})

	t.Run("error", func(t *testing.T) {
		ts := streamingTestServer(t, func(conn *websocket.Conn, _ streamWireFrame) error {
			return writeBackendFrame(conn, streaming.FrameError, map[string]any{
				"code": "execution_failed", "message": "backend rejected execution",
			})
		})
		conn := dialStreamingGateway(t, ts)

		sendExecutionFrame(t, conn, "error")
		frame := readStreamFrame(t, conn)
		assert.Equal(t, streaming.FrameError, frame.Type)

		var errorData streaming.ErrorData
		require.NoError(t, json.Unmarshal(frame.Data, &errorData))
		assert.Equal(t, "execution_failed", errorData.Code)
		assert.Equal(t, "backend rejected execution", errorData.Message)
	})
}

func TestE2E_StreamingCancelStopsDelivery(t *testing.T) {
	release := make(chan struct{})
	ts := streamingTestServer(t, func(conn *websocket.Conn, _ streamWireFrame) error {
		if err := writeBackendFrame(conn, streaming.FrameStdout, map[string]any{"content": "before cancel"}); err != nil {
			return err
		}
		<-release
		if err := writeBackendFrame(conn, streaming.FrameStdout, map[string]any{"content": "after cancel"}); err != nil {
			return err
		}
		return writeBackendFrame(conn, streaming.FrameResult, map[string]any{"exitCode": 0})
	})
	conn := dialStreamingGateway(t, ts)

	sendExecutionFrame(t, conn, "cancel")
	first := readStreamFrame(t, conn)
	assert.Equal(t, streaming.FrameStdout, first.Type)

	require.NoError(t, conn.WriteJSON(map[string]any{"type": streaming.FrameCancel}))
	// A keepalive round trip acts as an ordering barrier: once it arrives, the
	// read loop has already processed the preceding cancel frame.
	require.NoError(t, conn.WriteJSON(map[string]any{"type": streaming.FrameKeepAlive}))
	keepAlive := readStreamFrame(t, conn)
	require.Equal(t, streaming.FrameKeepAlive, keepAlive.Type)
	close(release)

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, _, err := conn.ReadMessage()
	require.Error(t, err, "cancel must suppress all frames produced after it")
}

func TestE2E_StreamingBackpressure(t *testing.T) {
	const (
		frameCount  = 256
		payloadSize = 64 * 1024
	)

	burstStarted := make(chan struct{})
	burstDone := make(chan struct{})
	ts := streamingTestServer(t, func(conn *websocket.Conn, _ streamWireFrame) error {
		close(burstStarted)
		defer close(burstDone)

		padding := strings.Repeat("x", payloadSize)
		for i := range frameCount {
			content := fmt.Sprintf("%03d:%s", i, padding)
			if err := writeBackendFrame(conn, streaming.FrameStdout, map[string]any{"content": content}); err != nil {
				return err
			}
		}
		return writeBackendFrame(conn, streaming.FrameResult, map[string]any{"exitCode": 0})
	})
	conn := dialStreamingGateway(t, ts)

	sendExecutionFrame(t, conn, "backpressure")
	select {
	case <-burstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("backend did not start streaming")
	}

	// Hold the client reader so the 16 MiB burst fills the transport buffers and
	// propagates pressure to the backend WebSocket instead of buffering the
	// complete stream in memory.
	time.Sleep(200 * time.Millisecond)
	select {
	case <-burstDone:
		t.Fatal("backend completed the 16 MiB burst while the client was not reading")
	default:
	}

	for i := range frameCount {
		frame := readStreamFrame(t, conn)
		require.Equal(t, streaming.FrameStdout, frame.Type)

		var data streaming.StdoutData
		require.NoError(t, json.Unmarshal(frame.Data, &data))
		require.True(t, strings.HasPrefix(data.Content, fmt.Sprintf("%03d:", i)), "frame %d arrived out of order", i)
	}

	result := readStreamFrame(t, conn)
	assert.Equal(t, streaming.FrameResult, result.Type)
	select {
	case <-burstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("backend remained blocked after the client drained the stream")
	}
}
