// Package streaming provides WebSocket frame types, normalization, and relay
// for proxying code execution and terminal output between E2B SDK clients and backends.
package streaming

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Frame types for WebSocket communication.
const (
	// Client → Server
	FrameCodeExec  = "code/exec"
	FrameStdin     = "stdin"
	FrameCancel    = "cancel"
	FrameTermStart = "terminal:start"
	FrameTermInput = "terminal:input"
	FrameTermResize = "terminal:resize"

	// Server → Client
	FrameStdout    = "stdout"
	FrameStderr    = "stderr"
	FrameResult    = "result"
	FrameError     = "error"
	FrameKeepAlive = "keepAlive"
	FrameTermData  = "terminal:data"
	FrameFSEvent   = "fs:event"
)

// Frame represents a WebSocket message in the E2B protocol.
type Frame struct {
	Type string      `json:"type"`
	Data interface{} `json:"data,omitempty"`
}

// StdoutData is the payload for stdout/stderr frames.
type StdoutData struct {
	Content     string `json:"content"`
	Timestamp   string `json:"timestamp"`
	ExecutionID string `json:"executionID,omitempty"`
}

// ResultData is the payload for result frames.
type ResultData struct {
	ExitCode    int     `json:"exitCode"`
	ExecutionID string  `json:"executionID,omitempty"`
	Duration    float64 `json:"duration,omitempty"`
}

// ErrorData is the payload for error frames.
type ErrorData struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// CodeExecData is the payload for code execution requests.
type CodeExecData struct {
	Code     string `json:"code"`
	Language string `json:"language,omitempty"`
}

// NewStdoutFrame creates a stdout frame.
func NewStdoutFrame(content, executionID string) *Frame {
	return &Frame{
		Type: FrameStdout,
		Data: &StdoutData{
			Content:     content,
			Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
			ExecutionID: executionID,
		},
	}
}

// NewStderrFrame creates a stderr frame.
func NewStderrFrame(content, executionID string) *Frame {
	return &Frame{
		Type: FrameStderr,
		Data: &StdoutData{
			Content:     content,
			Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
			ExecutionID: executionID,
		},
	}
}

// NewResultFrame creates a result frame.
func NewResultFrame(exitCode int, executionID string, duration float64) *Frame {
	return &Frame{
		Type: FrameResult,
		Data: &ResultData{
			ExitCode:    exitCode,
			ExecutionID: executionID,
			Duration:    duration,
		},
	}
}

// NewErrorFrame creates an error frame.
func NewErrorFrame(code, message string) *Frame {
	return &Frame{
		Type: FrameError,
		Data: &ErrorData{
			Code:    code,
			Message: message,
		},
	}
}

// TermDataPayload is the payload for terminal data frames.
type TermDataPayload struct {
	Data      string `json:"data"`
	Timestamp string `json:"timestamp"`
}

// NewKeepAliveFrame creates a heartbeat frame.
func NewKeepAliveFrame() *Frame {
	return &Frame{Type: FrameKeepAlive}
}

// NewTermDataFrame creates a terminal data frame.
func NewTermDataFrame(data string) *Frame {
	return &Frame{
		Type: FrameTermData,
		Data: &TermDataPayload{
			Data:      data,
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
}

// Marshal serializes a frame to JSON bytes.
func (f *Frame) Marshal() ([]byte, error) {
	return json.Marshal(f)
}

// Unmarshal deserializes a frame from JSON bytes.
func Unmarshal(data []byte) (*Frame, error) {
	var f Frame
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("unmarshal frame: %w", err)
	}
	return &f, nil
}

// FrameSender sends frames to a WebSocket connection.
type FrameSender interface {
	SendFrame(frame *Frame) error
}

// BufferedSender buffers frames and sends them with backpressure control.
type BufferedSender struct {
	ch       chan *Frame
	sender   FrameSender
	done     chan struct{}
	closeOnce sync.Once
}

// NewBufferedSender creates a sender with the given buffer size.
func NewBufferedSender(sender FrameSender, bufferSize int) *BufferedSender {
	if bufferSize <= 0 {
		bufferSize = 1024
	}
	bs := &BufferedSender{
		ch:     make(chan *Frame, bufferSize),
		sender: sender,
		done:   make(chan struct{}),
	}
	go bs.run()
	return bs
}

// Send queues a frame for sending. Non-blocking; drops if buffer full.
func (bs *BufferedSender) Send(frame *Frame) error {
	select {
	case bs.ch <- frame:
		return nil
	case <-bs.done:
		return fmt.Errorf("sender closed")
	default:
		return fmt.Errorf("buffer full, frame dropped")
	}
}

// Close stops the sender.
func (bs *BufferedSender) Close() {
	bs.closeOnce.Do(func() {
		close(bs.done)
	})
}

func (bs *BufferedSender) run() {
	for {
		select {
		case frame := <-bs.ch:
			if err := bs.sender.SendFrame(frame); err != nil {
				// Connection error, stop sending
				return
			}
		case <-bs.done:
			// Drain remaining frames
			for {
				select {
				case frame := <-bs.ch:
					_ = bs.sender.SendFrame(frame)
				default:
					return
				}
			}
		}
	}
}
