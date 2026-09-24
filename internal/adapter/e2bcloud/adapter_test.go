package e2bcloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/api/dto"
	"github.com/e2bgateway/e2bgateway/internal/config"
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
	a := NewAdapterWithClient("e2b-cloud", client, nil)

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
	a := NewAdapterWithClient("e2b-cloud", client, nil)

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
	a := NewAdapterWithClient("e2b-cloud", client, nil)

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
	a := NewAdapterWithClient("e2b-cloud", client, nil)

	if err := a.KillSandbox(context.Background(), "test-sbx-1"); err != nil {
		t.Fatalf("KillSandbox() error: %v", err)
	}
}

func TestE2BCloudAdapter_RunCommand(t *testing.T) {
	ts := mockE2BServer(t)
	defer ts.Close()

	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key"})
	a := NewAdapterWithClient("e2b-cloud", client, nil)

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
	a := NewAdapterWithClient("e2b-cloud", client, nil)

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
	a := NewAdapterWithClient("e2b-cloud", client, nil)

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
	a := NewAdapterWithClient("e2b-cloud", client, nil)

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

// Error Path Tests

func TestE2BCloudAdapter_HTTP4xxError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(dto.ErrorResponse{
			Code:    404,
			Message: "Sandbox not found",
		})
	}))
	defer ts.Close()

	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key", MaxRetries: 0})
	a := NewAdapterWithClient("e2b-cloud", client, nil)

	_, err := a.GetSandbox(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected APIError, got %T", err)
	}
	if apiErr.StatusCode != 404 {
		t.Errorf("expected status 404, got %d", apiErr.StatusCode)
	}
}

func TestE2BCloudAdapter_HTTP5xxRetry(t *testing.T) {
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(dto.ErrorResponse{
				Code:    500,
				Message: "Internal server error",
			})
			return
		}
		// Succeed on 3rd attempt
		json.NewEncoder(w).Encode(dto.SandboxInfo{
			SandboxID:  "test-sbx-1",
			TemplateID: "base",
			State:      "running",
		})
	}))
	defer ts.Close()

	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key", MaxRetries: 3})
	a := NewAdapterWithClient("e2b-cloud", client, nil)

	sbx, err := a.GetSandbox(context.Background(), "test-sbx-1")
	if err != nil {
		t.Fatalf("GetSandbox() error after retries: %v", err)
	}
	if sbx.SandboxID != "test-sbx-1" {
		t.Errorf("expected sandbox ID 'test-sbx-1', got %s", sbx.SandboxID)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestE2BCloudAdapter_ContextCancellation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow response
		time.Sleep(2 * time.Second)
		json.NewEncoder(w).Encode(dto.SandboxInfo{
			SandboxID: "test-sbx-1",
		})
	}))
	defer ts.Close()

	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key", MaxRetries: 0})
	a := NewAdapterWithClient("e2b-cloud", client, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := a.GetSandbox(ctx, "test-sbx-1")
	if err == nil {
		t.Fatal("expected error due to context cancellation")
	}
}

func TestE2BCloudAdapter_MalformedJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not valid json"))
	}))
	defer ts.Close()

	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key", MaxRetries: 0})
	a := NewAdapterWithClient("e2b-cloud", client, nil)

	_, err := a.GetSandbox(context.Background(), "test-sbx-1")
	if err == nil {
		t.Fatal("expected error for malformed JSON response")
	}
}

func TestE2BCloudAdapter_CreateSandbox_EnvVars(t *testing.T) {
	var gotBody dto.SandboxCreateRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sandboxes" && r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &gotBody)
			json.NewEncoder(w).Encode(dto.SandboxCreateResponse{
				SandboxID:  "test-sbx-env",
				TemplateID: "base",
				ClientID:   "c1",
			})
			return
		}
		// GET /sandboxes/{id} for full info fetch
		json.NewEncoder(w).Encode(dto.SandboxInfo{
			SandboxID:  "test-sbx-env",
			TemplateID: "base",
		})
	}))
	defer ts.Close()

	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key"})
	a := NewAdapterWithClient("e2b-cloud", client, nil)

	_, err := a.CreateSandbox(context.Background(), &adapter.CreateSandboxRequest{
		TemplateID: "base",
		Envs:       map[string]string{"FOO": "bar"},
	})
	if err != nil {
		t.Fatalf("CreateSandbox() error: %v", err)
	}
	if gotBody.EnvVars == nil || gotBody.EnvVars["FOO"] != "bar" {
		t.Errorf("expected envVars passthrough, got %+v", gotBody.EnvVars)
	}
}

func TestE2BCloudClient_SetEnvs(t *testing.T) {
	var gotBody map[string]map[string]string
	var gotPath, gotMethod string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key"})

	err := client.SetEnvs(context.Background(), "sbx-1", map[string]string{"FOO": "bar"})
	if err != nil {
		t.Fatalf("SetEnvs() error: %v", err)
	}
	if gotPath != "/sandboxes/sbx-1/envs" || gotMethod != http.MethodPost {
		t.Errorf("expected POST /sandboxes/sbx-1/envs, got %s %s", gotMethod, gotPath)
	}
	if gotBody["envs"]["FOO"] != "bar" {
		t.Errorf("expected envs body {envs: {FOO: bar}}, got %+v", gotBody)
	}
}

// TestNewAdapter_APIKeyConfigVariants verifies that NewAdapter reads the API key
// from both lowercase ("apikey", as Viper stores YAML map keys) and camelCase
// ("apiKey") config variants, by asserting the X-API-Key header on captured requests.
func TestNewAdapter_APIKeyConfigVariants(t *testing.T) {
	tests := []struct {
		name      string
		configKey string
	}{
		{"lowercase key (Viper)", "apikey"},
		{"camelCase key (backward compat)", "apiKey"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotAPIKey string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAPIKey = r.Header.Get("X-API-Key")
				json.NewEncoder(w).Encode([]dto.SandboxInfo{
					{SandboxID: "test-sbx-1", TemplateID: "base", State: "running"},
				})
			}))
			defer ts.Close()

			wantAPIKey := "key-" + tt.configKey
			a, err := NewAdapter(config.BackendConfig{
				Name: "e2b-cloud",
				Config: map[string]interface{}{
					"endpoint":   ts.URL,
					tt.configKey: wantAPIKey,
				},
			}, nil)
			if err != nil {
				t.Fatalf("NewAdapter() error: %v", err)
			}

			if _, err := a.ListSandboxes(context.Background(), adapter.ListOptions{}); err != nil {
				t.Fatalf("ListSandboxes() error: %v", err)
			}
			if gotAPIKey != wantAPIKey {
				t.Errorf("expected X-API-Key %q, got %q", wantAPIKey, gotAPIKey)
			}
		})
	}
}
