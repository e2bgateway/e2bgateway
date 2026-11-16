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

package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/metrics"
)

func testCfg(enabled bool) config.MetricsConfig {
	return config.MetricsConfig{Enabled: enabled, Path: "/metrics", Prefix: "e2bgw"}
}

func TestInitRegistersAndExposes(t *testing.T) {
	metrics.Reset()
	m := metrics.Init(testCfg(true), metrics.Sources{})
	if m == nil || metrics.Current() == nil {
		t.Fatal("expected metrics to be initialized")
	}
	metrics.ObserveHTTP("/sandboxes", "POST", 201, 50*time.Millisecond)
	metrics.RecordLifecycle("mock", "create", "success")
	metrics.RecordEnvdProxy("mock", 401, time.Millisecond)
	metrics.RecordTokenFailure("mock", "invalid")
	metrics.RecordWarmPoolAcquire("opensandbox", "hit")
	metrics.RecordTLSReload("success")
	metrics.RecordTLSReloadFailure()
	metrics.RecordTLSExpiryWarning("expired")
	metrics.RecordTLSExpiryWarning("near_expiry")
	metrics.RecordTLSExpiryWarning("not_yet_valid")

	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("handler status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"e2bgw_http_requests_total",
		"e2bgw_http_request_duration_seconds",
		`e2bgw_sandbox_lifecycle_total{backend="mock",operation="create",result="success"}`,
		`e2bgw_envd_proxy_requests_total{backend="mock",status_class="4xx"}`,
		`e2bgw_access_token_validation_failures_total{backend="mock",reason="invalid"}`,
		`e2bgw_warm_pool_acquire_total{backend="opensandbox",result="hit"}`,
		`e2bgw_tls_certificate_reloads_total{result="success"}`,
		`e2bgw_tls_certificate_reload_failures_total`,
		`e2bgw_tls_certificate_expiry_warnings_total{threshold="expired"}`,
		`e2bgw_tls_certificate_expiry_warnings_total{threshold="near_expiry"}`,
		`e2bgw_tls_certificate_expiry_warnings_total{threshold="not_yet_valid"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
}

func TestDisabledIsNoop(t *testing.T) {
	metrics.Reset()
	if m := metrics.Init(testCfg(false), metrics.Sources{}); m != nil {
		t.Fatal("expected nil Metrics when disabled")
	}
	if metrics.Current() != nil {
		t.Fatal("expected nil global when disabled")
	}
	// Record functions must not panic when disabled.
	metrics.RecordLifecycle("mock", "create", "success")
	metrics.ObserveHTTP("/x", "GET", 200, time.Millisecond)
	metrics.RecordTLSReload("success")
	metrics.RecordTLSReloadFailure()
	metrics.RecordTLSExpiryWarning("expired")

	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled handler status = %d, want 404", rec.Code)
	}
}

func TestCustomPrefix(t *testing.T) {
	metrics.Reset()
	cfg := testCfg(true)
	cfg.Prefix = "mygw"
	metrics.Init(cfg, metrics.Sources{})
	metrics.RecordLifecycle("b", "delete", "error")
	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "mygw_sandbox_lifecycle_total") {
		t.Error("expected custom prefix as namespace")
	}
}

func TestObserveHTTPNormalizesMethod(t *testing.T) {
	metrics.Reset()
	t.Cleanup(metrics.Reset)
	metrics.Init(testCfg(true), metrics.Sources{})

	// Non-standard methods must collapse to OTHER so unauthenticated clients
	// cannot inflate label cardinality (CWE-400).
	metrics.ObserveHTTP("/x", "FROBNICATE", 405, time.Millisecond)
	metrics.ObserveHTTP("/x", "GET", 200, time.Millisecond)

	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `e2bgw_http_requests_total{method="OTHER",route="/x",status="405"}`) {
		t.Errorf("expected non-standard method to normalize to OTHER, got:\n%s", body)
	}
	if !strings.Contains(body, `method="GET"`) {
		t.Error("standard method must be preserved")
	}
}
