package e2bcloud

import (
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/e2bgateway/e2bgateway/internal/api/dto"
)

// WSProxy handles bidirectional WebSocket proxying between the client and E2B Cloud.
type WSProxy struct {
	client   *Client
	upgrader websocket.Upgrader
	dialer   *websocket.Dialer
}

// NewWSProxy creates a new WebSocket proxy.
func NewWSProxy(client *Client) *WSProxy {
	return &WSProxy{
		client: client,
		upgrader: websocket.Upgrader{
			CheckOrigin:     func(_ *http.Request) bool { return true },
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
		},
		dialer: &websocket.Dialer{
			HandshakeTimeout: 10 * time.Second,
			ReadBufferSize:   4096,
			WriteBufferSize:  4096,
		},
	}
}

// ProxyCodeStream upgrades the client WebSocket and proxies to the E2B Cloud envd WebSocket.
func (p *WSProxy) ProxyCodeStream(w http.ResponseWriter, r *http.Request, sandboxID string) error {
	// Get access token for direct envd connection
	tokenResp, err := p.client.GetAccessToken(r.Context(), sandboxID)
	if err != nil {
		return fmt.Errorf("getting access token: %w", err)
	}

	// Upgrade client connection
	clientConn, err := p.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return fmt.Errorf("upgrading client connection: %w", err)
	}
	defer func() { _ = clientConn.Close() }()

	// Connect to E2B Cloud envd WebSocket
	envdURL := p.buildEnvdURL(sandboxID, tokenResp.AccessToken)
	backendConn, _, err := p.dialer.Dial(envdURL, nil)
	if err != nil {
		return fmt.Errorf("connecting to envd: %w", err)
	}
	defer func() { _ = backendConn.Close() }()

	// Bidirectional relay
	p.relay(clientConn, backendConn)
	return nil
}

// ProxyTerminal proxies a terminal session WebSocket.
func (p *WSProxy) ProxyTerminal(w http.ResponseWriter, r *http.Request, sandboxID string) error {
	return p.ProxyCodeStream(w, r, sandboxID)
}

// buildEnvdURL constructs the envd WebSocket URL.
func (p *WSProxy) buildEnvdURL(sandboxID, accessToken string) string {
	u, _ := url.Parse(p.client.baseURL)
	u.Scheme = "wss"
	u.Path = "/sandboxes/" + sandboxID + "/ws"
	q := u.Query()
	q.Set("access_token", accessToken)
	u.RawQuery = q.Encode()
	return u.String()
}

// relay performs bidirectional message forwarding between two WebSocket connections.
func (p *WSProxy) relay(clientConn, backendConn *websocket.Conn) {
	errCh := make(chan error, 2)

	// Client → Backend
	go func() {
		defer func() {
			errCh <- nil
		}()
		for {
			msgType, data, err := clientConn.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					return
				}
				errCh <- fmt.Errorf("reading from client: %w", err)
				return
			}

			// Normalize E2B frame → backend frame (passthrough for E2B Cloud)
			if err := backendConn.WriteMessage(msgType, data); err != nil {
				errCh <- fmt.Errorf("writing to backend: %w", err)
				return
			}
		}
	}()

	// Backend → Client
	go func() {
		defer func() {
			errCh <- nil
		}()
		for {
			msgType, data, err := backendConn.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					return
				}
				errCh <- fmt.Errorf("reading from backend: %w", err)
				return
			}

			// Normalize backend frame → E2B frame (passthrough for E2B Cloud)
			if err := clientConn.WriteMessage(msgType, data); err != nil {
				errCh <- fmt.Errorf("writing to client: %w", err)
				return
			}
		}
	}()

	// Wait for either direction to finish
	<-errCh
}

// NormalizeFrame converts a backend-specific WebSocket frame to the E2B standard format.
// For E2B Cloud, this is a passthrough since the frames are already in E2B format.
func NormalizeFrame(data []byte, _ string) ([]byte, error) {
	// For E2B Cloud, no normalization needed — frames are already E2B format
	return data, nil
}

// FrameType represents the type of a WebSocket frame.
type FrameType string

const (
	FrameTypeStdout    FrameType = "stdout"
	FrameTypeStderr    FrameType = "stderr"
	FrameTypeExit      FrameType = "exit"
	FrameTypeTerminal  FrameType = "terminal:data"
	FrameTypeFSEvent   FrameType = "fs:event"
	FrameTypeError     FrameType = "error"
	FrameTypeKeepAlive FrameType = "keepAlive"
)

// Frame represents a normalized WebSocket frame in E2B format.
type Frame struct {
	Type FrameType   `json:"type"`
	ID   string      `json:"id,omitempty"`
	Data interface{} `json:"data,omitempty"`
}

// RelayStats tracks relay statistics.
type RelayStats struct {
	mu              sync.Mutex
	ClientToBackend int64
	BackendToClient int64
	BytesSent       int64
	BytesReceived   int64
	StartedAt       time.Time
}

// NewRelayStats creates a new RelayStats tracker.
func NewRelayStats() *RelayStats {
	return &RelayStats{StartedAt: time.Now()}
}

// RecordClientToBackend records a message sent from client to backend.
func (s *RelayStats) RecordClientToBackend(bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ClientToBackend++
	s.BytesSent += bytes
}

// RecordBackendToClient records a message sent from backend to client.
func (s *RelayStats) RecordBackendToClient(bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.BackendToClient++
	s.BytesReceived += bytes
}

// Ensure Frame and DTOs are used to avoid import errors.
var (
	_ = dto.WSMessage{}
	_ = FrameTypeStdout
)
