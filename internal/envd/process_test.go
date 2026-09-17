package envd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStartProcessAndWait(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/process.Process/Start" {
			t.Errorf("expected path /process.Process/Start, got %q", r.URL.Path)
			return
		}

		w.Header().Set("Content-Type", "application/connect+json")
		w.WriteHeader(http.StatusOK)

		// Send start event
		startEvent := StartResponse{
			Event: &ProcessEvent{
				Start: &StartEvent{},
			},
		}
		env1, _ := EncodeEnvelope(EnvelopeFlagNone, startEvent)
		w.Write(env1)

		// Send stdout data
		stdoutData := base64.StdEncoding.EncodeToString([]byte("hello world\n"))
		dataEvent := StartResponse{
			Event: &ProcessEvent{
				Data: &DataEvent{
					Stdout: stdoutData,
				},
			},
		}
		env2, _ := EncodeEnvelope(EnvelopeFlagNone, dataEvent)
		w.Write(env2)

		// Send end event
		endEvent := StartResponse{
			Event: &ProcessEvent{
				End: &EndEvent{
					ExitCode: 0,
				},
			},
		}
		env3, _ := EncodeEnvelope(EnvelopeFlagNone, endEvent)
		w.Write(env3)

		// Send end-stream
		trailer := StreamTrailer{}
		env4, _ := EncodeEnvelope(EnvelopeFlagEndStream, trailer)
		w.Write(env4)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
	})

	req := &StartProcessRequest{
		Process: &ProcessConfig{
			Cmd:  "echo",
			Args: []string{"hello", "world"},
		},
	}

	stdout, stderr, exitCode, err := client.StartProcessAndWait(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if stdout != "hello world\n" {
		t.Errorf("expected stdout 'hello world\\n', got %q", stdout)
	}

	if stderr != "" {
		t.Errorf("expected empty stderr, got %q", stderr)
	}

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
}

func TestRunCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		var req StartProcessRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req.Process.Cmd != "/bin/bash" {
			t.Errorf("expected cmd /bin/bash, got %q", req.Process.Cmd)
		}

		if len(req.Process.Args) != 3 || req.Process.Args[0] != "-l" || req.Process.Args[1] != "-c" {
			t.Errorf("expected args [-l -c command], got %v", req.Process.Args)
		}

		if req.Process.Cwd != "/tmp" {
			t.Errorf("expected cwd /tmp, got %q", req.Process.Cwd)
		}

		if req.Process.Envs["TEST_VAR"] != "test_value" {
			t.Errorf("expected env TEST_VAR=test_value, got %v", req.Process.Envs)
		}

		w.Header().Set("Content-Type", "application/connect+json")
		w.WriteHeader(http.StatusOK)

		// Send start event
		startEvent := StartResponse{Event: &ProcessEvent{Start: &StartEvent{}}}
		env1, _ := EncodeEnvelope(EnvelopeFlagNone, startEvent)
		w.Write(env1)

		// Send stdout
		stdoutData := base64.StdEncoding.EncodeToString([]byte("command output\n"))
		dataEvent := StartResponse{Event: &ProcessEvent{Data: &DataEvent{Stdout: stdoutData}}}
		env2, _ := EncodeEnvelope(EnvelopeFlagNone, dataEvent)
		w.Write(env2)

		// Send end
		endEvent := StartResponse{Event: &ProcessEvent{End: &EndEvent{ExitCode: 0}}}
		env3, _ := EncodeEnvelope(EnvelopeFlagNone, endEvent)
		w.Write(env3)

		// Send end-stream
		trailer := StreamTrailer{}
		env4, _ := EncodeEnvelope(EnvelopeFlagEndStream, trailer)
		w.Write(env4)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
	})

	stdout, stderr, exitCode, err := client.RunCommand(
		context.Background(),
		"echo command output",
		"/tmp",
		map[string]string{"TEST_VAR": "test_value"},
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if stdout != "command output\n" {
		t.Errorf("expected stdout 'command output\\n', got %q", stdout)
	}

	if stderr != "" {
		t.Errorf("expected empty stderr, got %q", stderr)
	}

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
}

func TestListProcesses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/process.Process/List" {
			t.Errorf("expected path /process.Process/List, got %q", r.URL.Path)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		resp := ListProcessesResponse{
			Processes: []*ProcessInfo{
				{PID: 1, Tag: "init"},
				{PID: 123, Tag: "bash"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
	})

	processes, err := client.ListProcesses(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(processes) != 2 {
		t.Fatalf("expected 2 processes, got %d", len(processes))
	}

	if processes[0].PID != 1 || processes[0].Tag != "init" {
		t.Errorf("expected process 1/init, got %d/%s", processes[0].PID, processes[0].Tag)
	}

	if processes[1].PID != 123 || processes[1].Tag != "bash" {
		t.Errorf("expected process 123/bash, got %d/%s", processes[1].PID, processes[1].Tag)
	}
}

func TestSendSignal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/process.Process/SendSignal" {
			t.Errorf("expected path /process.Process/SendSignal, got %q", r.URL.Path)
			return
		}

		var req SendSignalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req.Process.PID != 123 {
			t.Errorf("expected PID 123, got %d", req.Process.PID)
		}

		if req.Signal != 9 {
			t.Errorf("expected signal 9, got %d", req.Signal)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
	})

	err := client.SendSignal(context.Background(), 123, 9)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProcessStream_Read(t *testing.T) {
	// Create stream data
	startEvent := StartResponse{Event: &ProcessEvent{Start: &StartEvent{}}}
	env1, _ := EncodeEnvelope(EnvelopeFlagNone, startEvent)

	stdoutData := base64.StdEncoding.EncodeToString([]byte("test output"))
	dataEvent := StartResponse{
		Event: &ProcessEvent{
			Data: &DataEvent{Stdout: stdoutData},
		},
	}
	env2, _ := EncodeEnvelope(EnvelopeFlagNone, dataEvent)

	endEvent := StartResponse{
		Event: &ProcessEvent{
			End: &EndEvent{ExitCode: 42},
		},
	}
	env3, _ := EncodeEnvelope(EnvelopeFlagNone, endEvent)

	trailer := StreamTrailer{}
	env4, _ := EncodeEnvelope(EnvelopeFlagEndStream, trailer)

	var buf bytes.Buffer
	buf.Write(env1)
	buf.Write(env2)
	buf.Write(env3)
	buf.Write(env4)

	stream := &ProcessStream{
		reader: io.NopCloser(&buf),
	}

	// Read start event
	event, err := stream.Read()
	if err != nil {
		t.Fatalf("failed to read start event: %v", err)
	}
	if event.Start == nil {
		t.Error("expected start event")
	}

	// Read data event
	event, err = stream.Read()
	if err != nil {
		t.Fatalf("failed to read data event: %v", err)
	}
	if event.Data == nil {
		t.Error("expected data event")
	}

	// Read end event
	event, err = stream.Read()
	if err != nil {
		t.Fatalf("failed to read end event: %v", err)
	}
	if event.End == nil {
		t.Error("expected end event")
	}
	if event.End.ExitCode != 42 {
		t.Errorf("expected exit code 42, got %d", event.End.ExitCode)
	}

	stream.Close()
}
