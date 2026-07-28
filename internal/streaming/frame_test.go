package streaming

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestFrame_Marshal(t *testing.T) {
	f := NewStdoutFrame("hello world\n", "exec-1")

	data, err := f.Marshal()
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	// Verify JSON structure
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if raw["type"] != "stdout" {
		t.Errorf("expected type 'stdout', got %v", raw["type"])
	}

	dataMap, ok := raw["data"].(map[string]interface{})
	if !ok {
		t.Fatal("expected data to be a map")
	}
	if dataMap["content"] != "hello world\n" {
		t.Errorf("expected content 'hello world\\n', got %v", dataMap["content"])
	}
}

func TestFrame_Unmarshal(t *testing.T) {
	input := `{"type":"result","data":{"exitCode":0,"executionID":"exec-1","duration":1.5}}`

	f, err := Unmarshal([]byte(input))
	if err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if f.Type != FrameResult {
		t.Errorf("expected type '%s', got '%s'", FrameResult, f.Type)
	}
}

func TestNewResultFrame(t *testing.T) {
	f := NewResultFrame(0, "exec-1", 1.234)

	data, err := f.Marshal()
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var raw map[string]interface{}
	json.Unmarshal(data, &raw)

	dataMap := raw["data"].(map[string]interface{})
	if dataMap["exitCode"] != float64(0) {
		t.Errorf("expected exitCode 0, got %v", dataMap["exitCode"])
	}
}

func TestNewErrorFrame(t *testing.T) {
	f := NewErrorFrame("NotFound", "sandbox not found")

	if f.Type != FrameError {
		t.Errorf("expected type '%s', got '%s'", FrameError, f.Type)
	}

	data, _ := f.Marshal()
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)
	dataMap := raw["data"].(map[string]interface{})
	if dataMap["code"] != "NotFound" {
		t.Errorf("expected code 'NotFound', got %v", dataMap["code"])
	}
}

func TestNewKeepAliveFrame(t *testing.T) {
	f := NewKeepAliveFrame()
	if f.Type != FrameKeepAlive {
		t.Errorf("expected type '%s', got '%s'", FrameKeepAlive, f.Type)
	}
}

func TestUnmarshal_Invalid(t *testing.T) {
	_, err := Unmarshal([]byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

type mockSender struct {
	mu     sync.Mutex
	frames []*Frame
}

func (m *mockSender) SendFrame(f *Frame) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.frames = append(m.frames, f)
	return nil
}

func (m *mockSender) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.frames)
}

func TestBufferedSender(t *testing.T) {
	ms := &mockSender{}
	bs := NewBufferedSender(ms, 10)

	bs.Send(NewStdoutFrame("line1", "exec-1"))
	bs.Send(NewStdoutFrame("line2", "exec-1"))
	bs.Send(NewResultFrame(0, "exec-1", 0.1))

	// Give goroutine time to process
	bs.Close()
	time.Sleep(50 * time.Millisecond)

	if ms.count() != 3 {
		t.Errorf("expected 3 frames sent, got %d", ms.count())
	}
}

func TestBufferedSender_BufferFull(t *testing.T) {
	ms := &mockSender{}
	// Create a sender with buffer size 1 but don't start the relay
	bs := &BufferedSender{
		ch:     make(chan *Frame, 1),
		sender: ms,
		done:   make(chan struct{}),
	}

	// Fill buffer
	bs.ch <- NewKeepAliveFrame()

	// Next send should fail (buffer full)
	err := bs.Send(NewKeepAliveFrame())
	if err == nil {
		t.Error("expected error when buffer full")
	}
}

func TestFrameTypes(t *testing.T) {
	types := []string{
		FrameCodeExec, FrameStdin, FrameCancel,
		FrameStdout, FrameStderr, FrameResult, FrameError, FrameKeepAlive,
		FrameTermStart, FrameTermInput, FrameTermResize, FrameTermData,
		FrameFSEvent,
	}
	for _, typ := range types {
		if typ == "" {
			t.Error("frame type should not be empty")
		}
	}
}
