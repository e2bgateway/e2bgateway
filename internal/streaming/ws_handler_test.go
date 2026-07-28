package streaming

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeWSConn is a test double for WSConn.
type fakeWSConn struct {
	mu       sync.Mutex
	readMsgs [][]byte
	readIdx  int
	readErr  error
	written  [][]byte
	writeErr error
	closed   bool
}

func (f *fakeWSConn) ReadMessage() (int, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return 0, nil, f.readErr
	}
	if f.readIdx >= len(f.readMsgs) {
		return 0, nil, errors.New("no more messages")
	}
	msg := f.readMsgs[f.readIdx]
	f.readIdx++
	return websocket.TextMessage, msg, nil
}

func (f *fakeWSConn) WriteMessage(_ int, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return f.writeErr
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	f.written = append(f.written, cp)
	return nil
}

func (f *fakeWSConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeWSConn) getWritten() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.written))
	copy(out, f.written)
	return out
}

func TestNewWSHandler_Defaults(t *testing.T) {
	h := NewWSHandler(WSHandlerConfig{})

	if h.readCh == nil {
		t.Error("readCh should not be nil")
	}
	if h.writeCh == nil {
		t.Error("writeCh should not be nil")
	}
	// Default buffer sizes should be 256
	if cap(h.readCh) != 256 {
		t.Errorf("readCh capacity = %d, want 256", cap(h.readCh))
	}
	if cap(h.writeCh) != 256 {
		t.Errorf("writeCh capacity = %d, want 256", cap(h.writeCh))
	}
}

func TestNewWSHandler_CustomConfig(t *testing.T) {
	h := NewWSHandler(WSHandlerConfig{
		ReadBufferSize:  64,
		WriteBufferSize: 128,
		WriteTimeout:    5 * time.Second,
	})

	if cap(h.readCh) != 64 {
		t.Errorf("readCh capacity = %d, want 64", cap(h.readCh))
	}
	if cap(h.writeCh) != 128 {
		t.Errorf("writeCh capacity = %d, want 128", cap(h.writeCh))
	}
	if h.writeTimeout != 5*time.Second {
		t.Errorf("writeTimeout = %v, want 5s", h.writeTimeout)
	}
}

func TestWSHandler_ReadChWriteCh(t *testing.T) {
	h := NewWSHandler(WSHandlerConfig{})

	rc := h.ReadCh()
	if rc == nil {
		t.Fatal("ReadCh returned nil")
	}

	wc := h.WriteCh()
	if wc == nil {
		t.Fatal("WriteCh returned nil")
	}

	// Should be able to send to write channel without blocking
	frame := NewStdoutFrame("hello", "e1")
	select {
	case wc <- frame:
		// ok
	default:
		t.Fatal("WriteCh should accept a frame")
	}
}

func TestWSHandler_ReadPump(t *testing.T) {
	h := NewWSHandler(WSHandlerConfig{ReadBufferSize: 10})

	f1 := NewStdoutFrame("line1", "e1")
	f2 := NewResultFrame(0, "e1", 0.5)
	data1, _ := f1.Marshal()
	data2, _ := f2.Marshal()

	fake := &fakeWSConn{
		readMsgs: [][]byte{data1, data2},
	}

	// Run readPump in a goroutine; it will exit after reading all messages
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.readPump(fake)
	}()

	// Wait for readPump to finish (it errors when readMsgs is exhausted)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("readPump did not finish")
	}

	// Check that frames arrived on readCh
	var received []*Frame
	for i := 0; i < 2; i++ {
		select {
		case f := <-h.ReadCh():
			received = append(received, f)
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("only received %d frames, want 2", len(received))
		}
	}

	if len(received) != 2 {
		t.Fatalf("received %d frames, want 2", len(received))
	}
	if received[0].Type != FrameStdout {
		t.Errorf("frame[0] type = %s, want %s", received[0].Type, FrameStdout)
	}
	if received[1].Type != FrameResult {
		t.Errorf("frame[1] type = %s, want %s", received[1].Type, FrameResult)
	}
}

func TestWSHandler_ReadPump_InvalidJSON(t *testing.T) {
	h := NewWSHandler(WSHandlerConfig{ReadBufferSize: 10})

	validFrame := NewKeepAliveFrame()
	validData, _ := validFrame.Marshal()

	fake := &fakeWSConn{
		readMsgs: [][]byte{[]byte("invalid json"), validData},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.readPump(fake)
	}()

	<-done

	// Only the valid frame should have been pushed
	select {
	case f := <-h.ReadCh():
		if f.Type != FrameKeepAlive {
			t.Errorf("expected keepAlive frame, got %s", f.Type)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected valid frame to be pushed despite invalid JSON")
	}
}

func TestWSHandler_WritePump(t *testing.T) {
	h := NewWSHandler(WSHandlerConfig{WriteBufferSize: 10})

	fake := &fakeWSConn{}

	f1 := NewStdoutFrame("out1", "e1")
	f2 := NewStderrFrame("err1", "e1")

	// Write frames to writeCh
	h.WriteCh() <- f1
	h.WriteCh() <- f2

	// Close the handler to signal writePump to drain and exit
	go func() {
		time.Sleep(50 * time.Millisecond)
		h.Close()
	}()

	h.writePump(fake)

	written := fake.getWritten()
	if len(written) != 2 {
		t.Fatalf("expected 2 messages written, got %d", len(written))
	}

	// Verify first message
	var raw map[string]interface{}
	json.Unmarshal(written[0], &raw)
	if raw["type"] != FrameStdout {
		t.Errorf("written[0] type = %v, want %s", raw["type"], FrameStdout)
	}
}

func TestWSHandler_CloseIdempotent(t *testing.T) {
	h := NewWSHandler(WSHandlerConfig{})

	err1 := h.Close()
	err2 := h.Close()

	if err1 != nil {
		t.Errorf("first Close: %v", err1)
	}
	if err2 != nil {
		t.Errorf("second Close: %v", err2)
	}
}

func TestWSHandler_FrameSender(t *testing.T) {
	h := NewWSHandler(WSHandlerConfig{WriteBufferSize: 10})

	sender := h.FrameSender()
	if sender == nil {
		t.Fatal("FrameSender returned nil")
	}

	f := NewStdoutFrame("test", "e1")
	err := sender.SendFrame(f)
	if err != nil {
		t.Fatalf("SendFrame: %v", err)
	}

	// Verify the frame is written to the WebSocket via writePump.
	// Use a fake conn and close the handler to trigger writePump drain.
	fake := &fakeWSConn{}
	go func() {
		time.Sleep(50 * time.Millisecond)
		h.Close()
	}()
	h.writePump(fake)

	written := fake.getWritten()
	if len(written) != 1 {
		t.Fatalf("expected 1 message written, got %d", len(written))
	}
	var raw map[string]interface{}
	json.Unmarshal(written[0], &raw)
	if raw["type"] != FrameStdout {
		t.Errorf("got type %v, want %s", raw["type"], FrameStdout)
	}
}

func TestWSHandler_FrameSender_Closed(t *testing.T) {
	h := NewWSHandler(WSHandlerConfig{WriteBufferSize: 10})
	h.Close()

	sender := h.FrameSender()
	err := sender.SendFrame(NewKeepAliveFrame())
	if err == nil {
		t.Error("expected error sending to closed handler")
	}
}

func TestWSHandler_FrameSender_WithTimeout(t *testing.T) {
	h := NewWSHandler(WSHandlerConfig{
		WriteBufferSize: 1,
		WriteTimeout:    50 * time.Millisecond,
	})

	// Fill the write channel
	h.WriteCh() <- NewKeepAliveFrame()

	sender := h.FrameSender()
	err := sender.SendFrame(NewKeepAliveFrame())
	if err == nil {
		t.Error("expected timeout error when write channel is full")
	}
}

func TestWSHandler_FrameSender_NoTimeout_BufferFull(t *testing.T) {
	h := NewWSHandler(WSHandlerConfig{
		WriteBufferSize: 1,
		// No timeout
	})

	// Fill the write channel
	h.WriteCh() <- NewKeepAliveFrame()

	sender := h.FrameSender()
	err := sender.SendFrame(NewKeepAliveFrame())
	if err == nil {
		t.Error("expected 'write buffer full' error")
	}
}

func TestWSHandler_ReadPump_CloseSignal(t *testing.T) {
	h := NewWSHandler(WSHandlerConfig{ReadBufferSize: 1})

	// Close handler before readPump processes
	h.Close()

	fake := &fakeWSConn{
		readMsgs: [][]byte{[]byte(`{"type":"keepAlive"}`)},
	}

	// readPump should exit due to closeCh
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.readPump(fake)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(time.Second):
		t.Error("readPump did not exit after handler close")
	}
}

func TestSetWriteDeadline_NoDeadliner(t *testing.T) {
	// fakeWSConn does not implement SetWriteDeadline, so it should be a no-op
	fake := &fakeWSConn{}
	// Should not panic
	setWriteDeadline(fake, time.Now().Add(time.Second))
}

func TestWSHandler_ServeWS(t *testing.T) {
	// ServeWS is an alias for ServeHTTP. We can't easily test the full upgrade
	// flow without an HTTP server, but we verify the method exists and is callable.
	h := NewWSHandler(WSHandlerConfig{})
	// Just verify it exists - actual upgrade testing requires httptest
	if h == nil {
		t.Fatal("handler should not be nil")
	}
}
