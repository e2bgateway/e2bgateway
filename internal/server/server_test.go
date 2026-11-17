// Copyright The E2BGateway Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/metrics"
	"github.com/e2bgateway/e2bgateway/internal/server"
)

func newTestServer(t *testing.T) *server.Server {
	t.Helper()
	srv, err := server.New(newTestConfig(true))
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	return srv
}

// newTestConfig builds the shared test server config; metricsEnabled toggles
// the Prometheus endpoint.
func newTestConfig(metricsEnabled bool) *config.Config {
	return &config.Config{
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
			Metrics: config.MetricsConfig{Enabled: metricsEnabled, Path: "/metrics", Prefix: "e2bgw"},
		},
	}
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

	var body map[string]any
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

	var sbx map[string]any
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

	var sandboxes []map[string]any
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

func TestMetricsEndpoint(t *testing.T) {
	metrics.Reset()
	t.Cleanup(metrics.Reset)
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz") // generate one counted request
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	resp.Body.Close()

	// e2bgw_sandbox_active is a scrape-time gauge with no series until at
	// least one sandbox is tracked, so create one before scraping.
	resp, err = http.Post(ts.URL+"/api/v1/sandboxes", "application/json", strings.NewReader(`{"templateID":"base"}`))
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	resp.Body.Close()

	resp, err = http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"e2bgw_http_requests_total", "e2bgw_sandbox_active", "e2bgw_backend_health"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("metrics body missing %q", want)
		}
	}
}

// TestEnvdProxyRecordsTokenFailure sends a data-plane request to the catch-all
// envd proxy with a sandbox ID header but no access token, and verifies the
// token-failure counter is exposed on /metrics. Note the mock adapter's
// GetEnvdEndpoint always errors ("envd not available in mock adapter"), so a
// request can never reach the actual proxy.ServeHTTP stage under the mock
// backend; the proxy request/latency metrics are covered by the unit tests in
// internal/metrics instead.
func TestEnvdProxyRecordsTokenFailure(t *testing.T) {
	metrics.Reset()
	t.Cleanup(metrics.Reset)
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/files", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("E2b-Sandbox-Id", "sbx-nonexistent")
	// No X-Access-Token header: the handler must take the "missing" branch.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	resp2, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body), `e2bgw_access_token_validation_failures_total{backend="mock",reason="missing"}`) {
		t.Errorf("missing token failure metric:\n%s", body)
	}
}

func TestMetricsEndpointDisabled(t *testing.T) {
	metrics.Reset()
	t.Cleanup(metrics.Reset)
	srv, err := server.New(newTestConfig(false))
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	// Not registered as a metrics route: falls through to the envd catch-all
	// proxy, which answers 400 (missing sandbox ID) — the key assertion is
	// that no Prometheus body is served.
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "e2bgw_http_requests_total") {
		t.Error("metrics must not be exposed when disabled")
	}
}

func TestEnvdProxy_RoutingMissMultiBackend404(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{HTTP: config.HTTPConfig{Address: "127.0.0.1:0"}},
		Backends: []config.BackendConfig{
			{Name: "mock", Type: "mock", Enabled: true},
			// e2b-cloud constructs offline (endpoint defaults); a second
			// registered adapter is all the 404 branch needs.
			{Name: "e2b-test", Type: "e2b-cloud", Enabled: true},
		},
		Routing: config.RoutingConfig{DefaultBackend: "mock", Strategy: "static"},
	}
	srv, err := server.New(cfg)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/some-envd-path", nil)
	req.Header.Set("E2b-Sandbox-Id", "unknown-sandbox")
	req.Header.Set("X-Access-Token", "whatever")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for routing miss with multiple backends, got %d", resp.StatusCode)
	}
}

func TestEnvdProxy_RoutingMissSingleBackendFallsThrough(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Unknown sandbox but a single registered adapter: the request must reach
	// token validation (mock has no token for it ⇒ 401), NOT 404.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/some-envd-path", nil)
	req.Header.Set("E2b-Sandbox-Id", "unknown-sandbox")
	req.Header.Set("X-Access-Token", "whatever")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 (fell through to sole adapter's validation), got %d", resp.StatusCode)
	}
}
