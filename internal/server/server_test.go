package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/server"
)

func newTestServer(t *testing.T) *server.Server {
	t.Helper()
	cfg := &config.Config{
		Server: config.ServerConfig{
			HTTP: config.HTTPConfig{
				Address: "127.0.0.1:0",
			},
		},
		Backends: []config.BackendConfig{
			{Name: "mock", Type: "mock", Enabled: true},
		},
		Routing: config.RoutingConfig{
			DefaultBackend: "mock",
			Strategy:       "static",
		},
		Observability: config.ObservabilityConfig{
			Logging: config.LoggingConfig{Level: "debug"},
		},
	}

	srv, err := server.New(cfg)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	return srv
}

func TestServerHealthEndpoint(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestServerReadyEndpoint(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %v", body["status"])
	}
}

func TestServerSandboxCRUD(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Create sandbox
	createBody := `{"templateID": "base"}`
	resp, err := http.Post(ts.URL+"/api/v1/sandboxes", "application/json", strings.NewReader(createBody))
	if err != nil {
		t.Fatalf("POST /sandboxes error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var sbx map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&sbx)
	sandboxID, ok := sbx["sandboxID"].(string)
	if !ok || sandboxID == "" {
		t.Fatal("expected non-empty sandboxID in response")
	}

	// Get sandbox
	resp, err = http.Get(ts.URL + "/api/v1/sandboxes/" + sandboxID)
	if err != nil {
		t.Fatalf("GET /sandboxes/{id} error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// List sandboxes
	resp, err = http.Get(ts.URL + "/api/v1/sandboxes")
	if err != nil {
		t.Fatalf("GET /sandboxes error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var sandboxes []map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&sandboxes)
	if len(sandboxes) < 1 {
		t.Error("expected at least 1 sandbox in list")
	}

	// Delete sandbox
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/sandboxes/"+sandboxID, nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /sandboxes/{id} error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}

	// Verify deleted
	resp, err = http.Get(ts.URL + "/api/v1/sandboxes/" + sandboxID)
	if err != nil {
		t.Fatalf("GET deleted sandbox error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for deleted sandbox, got %d", resp.StatusCode)
	}
}

func TestServerListTemplates(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/templates")
	if err != nil {
		t.Fatalf("GET /templates error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var templates []adapter.Template
	json.NewDecoder(resp.Body).Decode(&templates)
	if len(templates) < 1 {
		t.Error("expected at least 1 template")
	}
}

func TestServerCORS(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodOptions, ts.URL+"/api/v1/sandboxes", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS error: %v", err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Error("expected CORS header Access-Control-Allow-Origin: *")
	}
}
