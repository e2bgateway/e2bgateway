package dto

import (
	"encoding/json"
	"testing"
)

func TestSandboxCreateRequest_JSON(t *testing.T) {
	req := SandboxCreateRequest{
		TemplateID: "base",
		Timeout:    300,
		Metadata:   map[string]string{"user": "test"},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var decoded SandboxCreateRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if decoded.TemplateID != "base" {
		t.Errorf("expected templateID 'base', got %s", decoded.TemplateID)
	}
	if decoded.Timeout != 300 {
		t.Errorf("expected timeout 300, got %d", decoded.Timeout)
	}
	if decoded.Metadata["user"] != "test" {
		t.Errorf("expected metadata user=test, got %s", decoded.Metadata["user"])
	}
}

func TestSandboxInfo_JSON(t *testing.T) {
	raw := `{
		"sandboxID": "abc123",
		"templateID": "base",
		"state": "running",
		"startedAt": "2026-01-01T00:00:00Z",
		"endAt": "2026-01-01T01:00:00Z",
		"cpuCount": 2,
		"memoryMB": 512,
		"diskSizeMB": 1024
	}`

	var info SandboxInfo
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if info.SandboxID != "abc123" {
		t.Errorf("expected sandboxID 'abc123', got %s", info.SandboxID)
	}
	if info.State != "running" {
		t.Errorf("expected state 'running', got %s", info.State)
	}
	if info.CPUCount != 2 {
		t.Errorf("expected cpuCount 2, got %d", info.CPUCount)
	}
	if info.DiskSizeMB != 1024 {
		t.Errorf("expected diskSizeMB 1024, got %d", info.DiskSizeMB)
	}
}

func TestErrorResponse_JSON(t *testing.T) {
	raw := `{"code":404,"message":"Sandbox 'abc' not found"}`

	var errResp ErrorResponse
	if err := json.Unmarshal([]byte(raw), &errResp); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if errResp.Code != 404 {
		t.Errorf("expected code 404, got %d", errResp.Code)
	}
	if errResp.Message != "Sandbox 'abc' not found" {
		t.Errorf("expected message, got %s", errResp.Message)
	}
}

func TestWSMessage_JSON(t *testing.T) {
	msg := WSMessage{
		Type: "stdout",
		ID:   "req-001",
		Data: "hello world\n",
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var decoded WSMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if decoded.Type != "stdout" {
		t.Errorf("expected type 'stdout', got %s", decoded.Type)
	}
	if decoded.ID != "req-001" {
		t.Errorf("expected id 'req-001', got %s", decoded.ID)
	}
}

func TestFileListResponse_JSON(t *testing.T) {
	raw := `{
		"entries": [
			{"name": "src", "type": "dir", "size": 4096},
			{"name": "main.py", "type": "file", "size": 1234}
		]
	}`

	var resp FileListResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if len(resp.Entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(resp.Entries))
	}
	if resp.Entries[0].Name != "src" {
		t.Errorf("expected first entry name 'src', got %s", resp.Entries[0].Name)
	}
	if resp.Entries[0].Type != "dir" {
		t.Errorf("expected first entry type 'dir', got %s", resp.Entries[0].Type)
	}
}

func TestTemplateBuildRequest_JSON(t *testing.T) {
	req := TemplateBuildRequest{
		Name:       "my-template",
		Dockerfile: "FROM python:3.11",
		StartCmd:   "python --version",
		CPUCount:   2,
		MemoryMB:   1024,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var decoded TemplateBuildRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if decoded.Name != "my-template" {
		t.Errorf("expected name 'my-template', got %s", decoded.Name)
	}
	if decoded.Dockerfile != "FROM python:3.11" {
		t.Errorf("expected dockerfile 'FROM python:3.11', got %s", decoded.Dockerfile)
	}
}
