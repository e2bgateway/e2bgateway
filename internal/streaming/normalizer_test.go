package streaming

import (
	"encoding/json"
	"testing"
)

func TestNormalizer_NormalizeStdout(t *testing.T) {
	n := NewNormalizer("exec-42")
	f := n.NormalizeStdout("hello world\n")

	if f.Type != FrameStdout {
		t.Errorf("expected type %s, got %s", FrameStdout, f.Type)
	}

	data, err := f.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	payload := raw["data"].(map[string]interface{})
	if payload["content"] != "hello world\n" {
		t.Errorf("content = %q, want %q", payload["content"], "hello world\n")
	}
	if payload["executionID"] != "exec-42" {
		t.Errorf("executionID = %v, want %v", payload["executionID"], "exec-42")
	}
	if _, ok := payload["timestamp"]; !ok {
		t.Error("timestamp should be present")
	}
}

func TestNormalizer_NormalizeStderr(t *testing.T) {
	n := NewNormalizer("exec-7")
	f := n.NormalizeStderr("error output")

	if f.Type != FrameStderr {
		t.Errorf("expected type %s, got %s", FrameStderr, f.Type)
	}

	data, _ := f.Marshal()
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)
	payload := raw["data"].(map[string]interface{})
	if payload["content"] != "error output" {
		t.Errorf("content = %q, want %q", payload["content"], "error output")
	}
	if payload["executionID"] != "exec-7" {
		t.Errorf("executionID = %v, want %v", payload["executionID"], "exec-7")
	}
}

func TestNormalizer_NormalizeExitCode(t *testing.T) {
	n := NewNormalizer("exec-9")
	f := n.NormalizeExitCode(137, 2.5)

	if f.Type != FrameResult {
		t.Errorf("expected type %s, got %s", FrameResult, f.Type)
	}

	data, _ := f.Marshal()
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)
	payload := raw["data"].(map[string]interface{})
	if payload["exitCode"] != float64(137) {
		t.Errorf("exitCode = %v, want 137", payload["exitCode"])
	}
	if payload["duration"] != float64(2.5) {
		t.Errorf("duration = %v, want 2.5", payload["duration"])
	}
	if payload["executionID"] != "exec-9" {
		t.Errorf("executionID = %v, want %v", payload["executionID"], "exec-9")
	}
}

func TestNormalizer_NormalizeError(t *testing.T) {
	n := NewNormalizer("exec-1")
	f := n.NormalizeError("Timeout", "execution timed out after 30s")

	if f.Type != FrameError {
		t.Errorf("expected type %s, got %s", FrameError, f.Type)
	}

	data, _ := f.Marshal()
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)
	payload := raw["data"].(map[string]interface{})
	if payload["code"] != "Timeout" {
		t.Errorf("code = %v, want Timeout", payload["code"])
	}
	if payload["message"] != "execution timed out after 30s" {
		t.Errorf("message = %v, want 'execution timed out after 30s'", payload["message"])
	}
}

func TestNormalizer_NormalizeTermData(t *testing.T) {
	n := NewNormalizer("exec-1")
	f := n.NormalizeTermData("\x1b[31mred text\x1b[0m")

	if f.Type != FrameTermData {
		t.Errorf("expected type %s, got %s", FrameTermData, f.Type)
	}

	data, _ := f.Marshal()
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)
	payload := raw["data"].(map[string]interface{})
	if payload["data"] != "\x1b[31mred text\x1b[0m" {
		t.Errorf("data = %q, want ANSI escape payload", payload["data"])
	}
	if _, ok := payload["timestamp"]; !ok {
		t.Error("timestamp should be present")
	}
}

func TestNormalizer_EmptyExecutionID(t *testing.T) {
	n := NewNormalizer("")
	f := n.NormalizeStdout("output")

	data, _ := f.Marshal()
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)
	payload := raw["data"].(map[string]interface{})
	// executionID is empty, and StdoutData uses omitempty on ExecutionID,
	// so the key will be absent from JSON (appears as nil in the map).
	if payload["executionID"] != nil && payload["executionID"] != "" {
		t.Errorf("executionID = %v, want nil or empty string", payload["executionID"])
	}
}

func TestNormalizer_MultipleFrames(t *testing.T) {
	n := NewNormalizer("exec-multi")
	frames := []*Frame{
		n.NormalizeStdout("line1"),
		n.NormalizeStdout("line2"),
		n.NormalizeStderr("warn"),
		n.NormalizeExitCode(0, 0.5),
		n.NormalizeTermData("prompt$ "),
	}

	expectedTypes := []string{FrameStdout, FrameStdout, FrameStderr, FrameResult, FrameTermData}
	for i, f := range frames {
		if f.Type != expectedTypes[i] {
			t.Errorf("frame[%d] type = %s, want %s", i, f.Type, expectedTypes[i])
		}
	}
}
