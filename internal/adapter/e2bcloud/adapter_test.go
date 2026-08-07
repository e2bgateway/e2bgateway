package e2bcloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/api/dto"
)

// mockE2BServer creates a test HTTP server that mimics the E2B Cloud API.
func mockE2BServer(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()

	// Sandbox endpoints
	mux.HandleFunc("/sandboxes", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			json.NewEncoder(w).Encode(dto.SandboxCreateResponse{
				SandboxID:  "test-sbx-1",
				TemplateID: "base",
				ClientID:   "test-client",
			})
		case http.MethodGet:
			json.NewEncoder(w).Encode([]dto.SandboxInfo{
				{
					SandboxID:  "test-sbx-1",
					TemplateID: "base",
					State:      "running",
				},
			})
		}
	})

	mux.HandleFunc("/sandboxes/test-sbx-1", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(dto.SandboxInfo{
				SandboxID:  "test-sbx-1",
				TemplateID: "base",
				State:      "running",
			})
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	})

	mux.HandleFunc("/sandboxes/test-sbx-1/pause", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/sandboxes/test-sbx-1/resume", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(dto.SandboxCreateResponse{
			SandboxID:  "test-sbx-1",
			TemplateID: "base",
		})
	})

	mux.HandleFunc("/sandboxes/test-sbx-1/timeout", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/sandboxes/test-sbx-1/commands", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(dto.CommandResult{
			Stdout:   "hello world\n",
			ExitCode: 0,
		})
	})

	mux.HandleFunc("/sandboxes/test-sbx-1/code", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(dto.CodeExecResult{
			Stdout:   "code output\n",
			ExitCode: 0,
		})
	})

	mux.HandleFunc("/sandboxes/test-sbx-1/access-token", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(dto.AccessTokenResponse{
			AccessToken: "test-token",
		})
	})

	// Template endpoints
	mux.HandleFunc("/templates", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode([]dto.TemplateInfo{
				{TemplateID: "base", Public: true, Ready: true},
			})
		case http.MethodPost:
			json.NewEncoder(w).Encode(dto.TemplateBuildResponse{
				TemplateID: "new-template",
				BuildID:    "build-1",
				Status:     "building",
			})
		}
	})

	mux.HandleFunc("/templates/base", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(dto.TemplateInfo{
			TemplateID: "base",
			Public:     true,
			Ready:      true,
		})
	})

	// Warm pool endpoints
	mux.HandleFunc("/warm-pools", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]dto.WarmPoolInfo{})
	})

	return httptest.NewServer(mux)
}

func TestE2BCloudAdapter_CreateSandbox(t *testing.T) {
	ts := mockE2BServer(t)
	defer ts.Close()

	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key"})
	a := NewAdapterWithClient("e2b-cloud", client)

	sbx, err := a.CreateSandbox(context.Background(), &adapter.CreateSandboxRequest{
		TemplateID: "base",
	})
	if err != nil {
		t.Fatalf("CreateSandbox() error: %v", err)
	}
	if sbx.SandboxID != "test-sbx-1" {
		t.Errorf("expected sandbox ID 'test-sbx-1', got %s", sbx.SandboxID)
	}
	if sbx.Backend != "e2b-cloud" {
		t.Errorf("expected backend 'e2b-cloud', got %s", sbx.Backend)
	}
}

func TestE2BCloudAdapter_ListSandboxes(t *testing.T) {
	ts := mockE2BServer(t)
	defer ts.Close()

	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key"})
	a := NewAdapterWithClient("e2b-cloud", client)

	sandboxes, err := a.ListSandboxes(context.Background(), adapter.ListOptions{})
	if err != nil {
		t.Fatalf("ListSandboxes() error: %v", err)
	}
	if len(sandboxes) != 1 {
		t.Errorf("expected 1 sandbox, got %d", len(sandboxes))
	}
	if sandboxes[0].SandboxID != "test-sbx-1" {
		t.Errorf("expected sandbox ID 'test-sbx-1', got %s", sandboxes[0].SandboxID)
	}
}

func TestE2BCloudAdapter_GetSandbox(t *testing.T) {
	ts := mockE2BServer(t)
	defer ts.Close()

	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key"})
	a := NewAdapterWithClient("e2b-cloud", client)

	sbx, err := a.GetSandbox(context.Background(), "test-sbx-1")
	if err != nil {
		t.Fatalf("GetSandbox() error: %v", err)
	}
	if sbx.Status != adapter.SandboxStatusRunning {
		t.Errorf("expected status running, got %s", sbx.Status)
	}
}

func TestE2BCloudAdapter_KillSandbox(t *testing.T) {
	ts := mockE2BServer(t)
	defer ts.Close()

	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key"})
	a := NewAdapterWithClient("e2b-cloud", client)

	if err := a.KillSandbox(context.Background(), "test-sbx-1"); err != nil {
		t.Fatalf("KillSandbox() error: %v", err)
	}
}

func TestE2BCloudAdapter_RunCommand(t *testing.T) {
	ts := mockE2BServer(t)
	defer ts.Close()

	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key"})
	a := NewAdapterWithClient("e2b-cloud", client)

	result, err := a.RunCommand(context.Background(), "test-sbx-1", &adapter.CommandRequest{
		Command: "echo hello",
	})
	if err != nil {
		t.Fatalf("RunCommand() error: %v", err)
	}
	if result.Stdout != "hello world\n" {
		t.Errorf("expected stdout 'hello world\\n', got %q", result.Stdout)
	}
}

func TestE2BCloudAdapter_ExecuteCode(t *testing.T) {
	ts := mockE2BServer(t)
	defer ts.Close()

	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key"})
	a := NewAdapterWithClient("e2b-cloud", client)

	result, err := a.ExecuteCode(context.Background(), "test-sbx-1", &adapter.CodeExecutionRequest{
		Code: "print('hello')",
	})
	if err != nil {
		t.Fatalf("ExecuteCode() error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
}

func TestE2BCloudAdapter_ListTemplates(t *testing.T) {
	ts := mockE2BServer(t)
	defer ts.Close()

	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key"})
	a := NewAdapterWithClient("e2b-cloud", client)

	templates, err := a.ListTemplates(context.Background(), adapter.ListOptions{})
	if err != nil {
		t.Fatalf("ListTemplates() error: %v", err)
	}
	if len(templates) != 1 {
		t.Errorf("expected 1 template, got %d", len(templates))
	}
}

func TestE2BCloudAdapter_HealthCheck(t *testing.T) {
	ts := mockE2BServer(t)
	defer ts.Close()

	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key"})
	a := NewAdapterWithClient("e2b-cloud", client)

	if err := a.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck() error: %v", err)
	}
}

func TestAPIError(t *testing.T) {
	err := &APIError{
		StatusCode: 404,
		Code:       404,
		Message:    "Sandbox 'abc' not found",
	}
	if !err.IsNotFound() {
		t.Error("expected IsNotFound() to return true for 404")
	}
	if err.StatusCode != 404 {
		t.Errorf("expected status 404, got %d", err.StatusCode)
	}
}
