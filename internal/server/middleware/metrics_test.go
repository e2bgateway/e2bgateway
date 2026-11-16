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

package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/metrics"
	gwmiddleware "github.com/e2bgateway/e2bgateway/internal/server/middleware"
)

func initMetrics(t *testing.T) {
	t.Helper()
	metrics.Reset()
	t.Cleanup(metrics.Reset)
	metrics.Init(config.MetricsConfig{Enabled: true, Path: "/metrics", Prefix: "e2bgw"}, metrics.Sources{})
}

func scrape(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

func TestMetricsMiddlewareCountsRequests(t *testing.T) {
	initMetrics(t)
	h := gwmiddleware.Metrics("")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/anything", nil))

	body := scrape(t)
	if !strings.Contains(body, `e2bgw_http_requests_total{method="GET",route="unmatched",status="418"}`) {
		t.Errorf("expected request counter, got:\n%s", body)
	}
}

func TestMetricsMiddlewareSkipsMetricsPath(t *testing.T) {
	initMetrics(t)
	h := gwmiddleware.Metrics("")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if strings.Contains(scrape(t), `route="/metrics"`) {
		t.Error("metrics endpoint must not count itself")
	}
}

func TestMetricsMiddlewareNoopWhenDisabled(t *testing.T) {
	metrics.Reset()
	t.Cleanup(metrics.Reset)
	called := false
	h := gwmiddleware.Metrics("")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if !called {
		t.Fatal("next handler must still run when metrics disabled")
	}
}

func TestMetricsMiddlewareSkipsCustomScrapePath(t *testing.T) {
	initMetrics(t)
	h := gwmiddleware.Metrics("/internal/metrics")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// The scrape-path request must not be recorded at all. Without a chi
	// route context every recorded request lands on route="unmatched", so
	// assert on the counter's absence/value, not just the route label.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/internal/metrics", nil))
	if strings.Contains(scrape(t), "e2bgw_http_requests_total") {
		t.Error("custom scrape path must not count itself")
	}

	// Exactly one counted request after the non-scrape call.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/other", nil))
	body := scrape(t)
	if !strings.Contains(body, `e2bgw_http_requests_total{method="GET",route="unmatched",status="200"} 1`) {
		t.Errorf("expected exactly one counted request, got:\n%s", body)
	}
}

func TestMetricsMiddlewareCountsRecoveredPanics(t *testing.T) {
	initMetrics(t)
	// Chain order used by the server: Metrics OUTSIDE Recovery, so the
	// recovered 500 is observed by Metrics.
	h := gwmiddleware.Metrics("")(gwmiddleware.Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/panic", nil))
	if !strings.Contains(scrape(t), `e2bgw_http_requests_total{method="GET",route="unmatched",status="500"}`) {
		t.Error("recovered panic must be recorded as 500")
	}
}
