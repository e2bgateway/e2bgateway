package streaming

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WSConn abstracts a WebSocket connection so the handler can be tested with
// fakes. The real implementation wraps *websocket.Conn.
type WSConn interface {
	ReadMessage() (messageType int, p []byte, err error)
	WriteMessage(messageType int, data []byte) error
	Close() error
}

// WSHandler upgrades HTTP connections to WebSocket and provides channels for
// reading and writing frames.
type WSHandler struct {
	upgrader    websocket.Upgrader
	readCh      chan *Frame
	writeCh     chan *Frame
	readBufSize int
	writeBufSize int
	mu          sync.Mutex
	conn        WSConn
	closed      bool
	closeCh     chan struct{}
	writeTimeout time.Duration
}

// WSHandlerConfig holds options for constructing a WSHandler.
type WSHandlerConfig struct {
	// ReadBufferSize is the capacity of the inbound frame channel (default 256).
	ReadBufferSize int
	// WriteBufferSize is the capacity of the outbound frame channel (default 256).
	WriteBufferSize int
	// WriteTimeout is the maximum time to wait for a single WebSocket write.
	// Zero means no timeout.
	WriteTimeout time.Duration
}

// NewWSHandler creates a WSHandler with the given configuration.
func NewWSHandler(cfg WSHandlerConfig) *WSHandler {
	if cfg.ReadBufferSize <= 0 {
		cfg.ReadBufferSize = 256
	}
	if cfg.WriteBufferSize <= 0 {
		cfg.WriteBufferSize = 256
	}
	return &WSHandler{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		readCh:       make(chan *Frame, cfg.ReadBufferSize),
		writeCh:      make(chan *Frame, cfg.WriteBufferSize),
		readBufSize:  cfg.ReadBufferSize,
		writeBufSize: cfg.WriteBufferSize,
		closeCh:      make(chan struct{}),
		writeTimeout: cfg.WriteTimeout,
	}
}

// ServeHTTP upgrades the HTTP connection to WebSocket and starts the read/write
// goroutines. It blocks until the connection is closed or the context is
// canceled.
func (h *WSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = conn.Close()
		return
	}
	h.conn = conn
	h.mu.Unlock()

	ctx := r.Context()

	// readPump reads WebSocket messages, unmarshals them into Frames, and
	// pushes them onto ReadCh().
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		h.readPump(conn)
	}()

	// writePump reads from WriteCh() and writes them to the WebSocket.
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		h.writePump(conn)
	}()

	select {
	case <-readDone:
	case <-writeDone:
	case <-ctx.Done():
	}

	_ = h.close()
	_ = conn.Close()
}

// ServeWS upgrades an existing http.ResponseWriter/Request pair. This is
// identical to ServeHTTP but is provided for convenience when using the
// handler with non-standard routers.
func (h *WSHandler) ServeWS(w http.ResponseWriter, r *http.Request) {
	h.ServeHTTP(w, r)
}

// ReadCh returns the channel that receives frames read from the WebSocket.
func (h *WSHandler) ReadCh() <-chan *Frame {
	return h.readCh
}

// WriteCh returns the channel to which frames should be sent for writing to
// the WebSocket.
func (h *WSHandler) WriteCh() chan<- *Frame {
	return h.writeCh
}

// FrameSender returns a FrameSender that writes frames through the WebSocket.
// Frames are marshaled to JSON and written as text messages.
func (h *WSHandler) FrameSender() FrameSender {
	return &wsFrameSender{
		ch:       h.writeCh,
		closeCh:  h.closeCh,
		timeout:  h.writeTimeout,
	}
}

// Close shuts down the handler, closing the WebSocket connection and internal
// channels. It is safe to call multiple times.
func (h *WSHandler) Close() error {
	return h.close()
}

func (h *WSHandler) close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	h.closed = true
	close(h.closeCh)
	if h.conn != nil {
		return h.conn.Close()
	}
	return nil
}

func (h *WSHandler) readPump(conn WSConn) {
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		frame, err := Unmarshal(msg)
		if err != nil {
			continue
		}
		select {
		case h.readCh <- frame:
		case <-h.closeCh:
			return
		default:
			// Read channel full, drop the frame.
		}
	}
}

func (h *WSHandler) writePump(conn WSConn) {
	for {
		select {
		case frame := <-h.writeCh:
			data, err := frame.Marshal()
			if err != nil {
				continue
			}
			if h.writeTimeout > 0 {
				setWriteDeadline(conn, time.Now().Add(h.writeTimeout))
			}
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-h.closeCh:
			// Drain remaining frames before exiting.
			for {
				select {
				case frame := <-h.writeCh:
					data, err := frame.Marshal()
					if err != nil {
						continue
					}
					_ = conn.WriteMessage(websocket.TextMessage, data)
				default:
					return
				}
			}
		}
	}
}

// wsFrameSender implements FrameSender by writing frames into a channel.
type wsFrameSender struct {
	ch      chan<- *Frame
	closeCh <-chan struct{}
	timeout time.Duration
}

func (s *wsFrameSender) SendFrame(frame *Frame) error {
	if s.timeout > 0 {
		timer := time.NewTimer(s.timeout)
		defer timer.Stop()
		select {
		case s.ch <- frame:
			return nil
		case <-timer.C:
			return fmt.Errorf("send frame timed out")
		case <-s.closeCh:
			return fmt.Errorf("connection closed")
		}
	}
	// Check closeCh first to avoid a race where both ch and closeCh are ready.
	select {
	case <-s.closeCh:
		return fmt.Errorf("connection closed")
	default:
	}
	select {
	case s.ch <- frame:
		return nil
	case <-s.closeCh:
		return fmt.Errorf("connection closed")
	default:
		return fmt.Errorf("write buffer full")
	}
}

// SetWriteDeadline is used internally by writePump. It is a no-op when the
// underlying WSConn does not support it (e.g. in tests with a fake).
// We rely on the fact that the real *websocket.Conn has this method; for fakes
// we ignore the error.
func setWriteDeadline(conn WSConn, t time.Time) {
	type deadliner interface {
		SetWriteDeadline(time.Time) error
	}
	if d, ok := conn.(deadliner); ok {
		_ = d.SetWriteDeadline(t)
	}
}
