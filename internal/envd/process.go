package envd

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
)

// Process service name
const ProcessService = "process.Process"

// StartProcessRequest is the request for process.Process/Start.
type StartProcessRequest struct {
	Process *ProcessConfig `json:"process"`
	PTY     *PTYConfig     `json:"pty,omitempty"`
	Tag     string         `json:"tag,omitempty"`
	Stdin   bool           `json:"stdin,omitempty"`
}

// ProcessConfig configures the process to start.
type ProcessConfig struct {
	Cmd  string            `json:"cmd"`
	Args []string          `json:"args,omitempty"`
	Envs map[string]string `json:"envs,omitempty"`
	Cwd  string            `json:"cwd,omitempty"`
}

// PTYConfig configures PTY allocation.
type PTYConfig struct {
	Size *PTYSize `json:"size,omitempty"`
}

// PTYSize specifies PTY dimensions.
type PTYSize struct {
	Cols uint32 `json:"cols"`
	Rows uint32 `json:"rows"`
}

// StartResponse is the response for process.Process/Start (streaming).
type StartResponse struct {
	Event *ProcessEvent `json:"event,omitempty"`
}

// ProcessEvent represents an event in the process stream.
type ProcessEvent struct {
	Start     *StartEvent     `json:"start,omitempty"`
	Data      *DataEvent      `json:"data,omitempty"`
	End       *EndEvent       `json:"end,omitempty"`
	Keepalive *KeepaliveEvent `json:"keepalive,omitempty"`
}

// StartEvent indicates the process has started.
type StartEvent struct{}

// DataEvent contains process output data.
type DataEvent struct {
	Stdout string `json:"stdout,omitempty"` // base64-encoded
	Stderr string `json:"stderr,omitempty"` // base64-encoded
	PTY    string `json:"pty,omitempty"`    // base64-encoded
}

// EndEvent indicates the process has ended.
type EndEvent struct {
	ExitCode int32  `json:"exitCode"`
	Error    string `json:"error,omitempty"`
}

// KeepaliveEvent is a keepalive signal.
type KeepaliveEvent struct{}

// ListProcessesRequest is the request for process.Process/List.
type ListProcessesRequest struct{}

// ListProcessesResponse is the response for process.Process/List.
type ListProcessesResponse struct {
	Processes []*ProcessInfo `json:"processes"`
}

// ProcessInfo contains information about a running process.
type ProcessInfo struct {
	PID    int32          `json:"pid"`
	Tag    string         `json:"tag,omitempty"`
	Config *ProcessConfig `json:"config,omitempty"`
}

// SendSignalRequest is the request for process.Process/SendSignal.
type SendSignalRequest struct {
	Process *ProcessSelector `json:"process"`
	Signal  int32            `json:"signal"`
}

// ProcessSelector selects a process by PID or tag.
type ProcessSelector struct {
	PID int32  `json:"pid,omitempty"`
	Tag string `json:"tag,omitempty"`
}

// StartProcess starts a new process in the sandbox and streams output.
// Returns a reader for the streaming response and the process start event.
func (c *Client) StartProcess(ctx context.Context, req *StartProcessRequest) (*ProcessStream, error) {
	stream, err := c.doConnectRPCStream(ctx, ProcessService, "Start", req)
	if err != nil {
		return nil, fmt.Errorf("starting process: %w", err)
	}

	return &ProcessStream{
		reader: stream,
	}, nil
}

// StartProcessAndWait starts a process and waits for it to complete.
// Returns stdout, stderr, and exit code.
func (c *Client) StartProcessAndWait(ctx context.Context, req *StartProcessRequest) (stdout, stderr string, exitCode int32, err error) {
	stream, err := c.StartProcess(ctx, req)
	if err != nil {
		return "", "", -1, err
	}
	defer stream.Close()

	return stream.ReadAll()
}

// RunCommand is a convenience method to run a shell command and return the output.
func (c *Client) RunCommand(ctx context.Context, command string, cwd string, envs map[string]string) (stdout, stderr string, exitCode int32, err error) {
	req := &StartProcessRequest{
		Process: &ProcessConfig{
			Cmd:  "/bin/bash",
			Args: []string{"-l", "-c", command},
			Cwd:  cwd,
			Envs: envs,
		},
	}

	return c.StartProcessAndWait(ctx, req)
}

// ListProcesses lists all running processes in the sandbox.
func (c *Client) ListProcesses(ctx context.Context) ([]*ProcessInfo, error) {
	var resp ListProcessesResponse
	if err := c.doConnectRPC(ctx, ProcessService, "List", &ListProcessesRequest{}, &resp); err != nil {
		return nil, fmt.Errorf("listing processes: %w", err)
	}
	return resp.Processes, nil
}

// SendSignal sends a signal to a process.
func (c *Client) SendSignal(ctx context.Context, pid int32, signal int32) error {
	req := &SendSignalRequest{
		Process: &ProcessSelector{PID: pid},
		Signal:  signal,
	}
	if err := c.doConnectRPC(ctx, ProcessService, "SendSignal", req, nil); err != nil {
		return fmt.Errorf("sending signal: %w", err)
	}
	return nil
}

// ProcessStream represents a streaming response from process.Process/Start.
type ProcessStream struct {
	reader   io.ReadCloser
	buffer   bytes.Buffer
	stdout   strings.Builder
	stderr   strings.Builder
	exitCode int32
	done     bool
}

// Read reads the next event from the stream.
func (s *ProcessStream) Read() (*ProcessEvent, error) {
	if s.done {
		return nil, io.EOF
	}

	env, err := DecodeEnvelope(s.reader)
	if err == io.EOF {
		s.done = true
		return nil, io.EOF
	}
	if err != nil {
		return nil, fmt.Errorf("decoding envelope: %w", err)
	}

	var resp StartResponse
	if err := env.UnmarshalPayload(&resp); err != nil {
		return nil, fmt.Errorf("unmarshaling event: %w", err)
	}

	if env.IsEndStream() {
		s.done = true
	}

	return resp.Event, nil
}

// ReadAll reads all events from the stream and returns stdout, stderr, and exit code.
func (s *ProcessStream) ReadAll() (stdout, stderr string, exitCode int32, err error) {
	for {
		event, err := s.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", "", -1, err
		}

		if event == nil {
			continue
		}

		if event.Data != nil {
			if event.Data.Stdout != "" {
				decoded, err := base64.StdEncoding.DecodeString(event.Data.Stdout)
				if err == nil {
					s.stdout.Write(decoded)
				}
			}
			if event.Data.Stderr != "" {
				decoded, err := base64.StdEncoding.DecodeString(event.Data.Stderr)
				if err == nil {
					s.stderr.Write(decoded)
				}
			}
		}

		if event.End != nil {
			s.exitCode = event.End.ExitCode
			if event.End.Error != "" {
				return s.stdout.String(), s.stderr.String(), s.exitCode, fmt.Errorf("process error: %s", event.End.Error)
			}
		}
	}

	return s.stdout.String(), s.stderr.String(), s.exitCode, nil
}

// Close closes the stream.
func (s *ProcessStream) Close() error {
	if s.reader != nil {
		return s.reader.Close()
	}
	return nil
}

// ExitCode returns the exit code of the process (available after stream completes).
func (s *ProcessStream) ExitCode() int32 {
	return s.exitCode
}
