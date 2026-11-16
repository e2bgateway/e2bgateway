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

// Package metrics provides the gateway's Prometheus instrumentation.
//
// All recording helpers are safe to call when metrics are disabled or not
// initialized (they become no-ops), so call sites need no nil checks.
package metrics

import (
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/e2bgateway/e2bgateway/internal/config"
)

// Default histogram buckets shared by HTTP and envd proxy latency metrics.
var latencyBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60}

// Sources provides live-read functions for gauge-style collectors evaluated
// at scrape time.
type Sources struct {
	SandboxCounts func() map[string]int  // backend -> active sandbox count
	BackendHealth func() map[string]bool // backend -> healthy
}

// Metrics holds the gateway's private Prometheus registry and instruments.
type Metrics struct {
	reg             *prometheus.Registry
	httpRequests    *prometheus.CounterVec
	httpDuration    *prometheus.HistogramVec
	lifecycle       *prometheus.CounterVec
	envdRequests    *prometheus.CounterVec
	envdDuration    *prometheus.HistogramVec
	tokenFailures   *prometheus.CounterVec
	warmPoolAcquire *prometheus.CounterVec
	sandboxActive   *prometheus.Desc
	backendHealth   *prometheus.Desc
	src             Sources
}

var global atomic.Pointer[Metrics]

// Init builds the instrument set and installs it as the process-global
// recorder. When cfg.Enabled is false the global is cleared and Init returns
// nil. Safe to call again (replaces the previous instance).
func Init(cfg config.MetricsConfig, src Sources) *Metrics {
	if !cfg.Enabled {
		global.Store(nil)
		return nil
	}
	ns := sanitizeNamespace(cfg.Prefix)
	m := &Metrics{
		reg:           prometheus.NewRegistry(),
		src:           src,
		sandboxActive: prometheus.NewDesc(ns+"_sandbox_active", "Active sandboxes tracked by the gateway, per backend.", []string{"backend"}, nil),
		backendHealth: prometheus.NewDesc(ns+"_backend_health", "Backend health as observed by the routing health checker (1 healthy, 0 unhealthy).", []string{"backend"}, nil),
	}
	m.httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "http_requests_total",
		Help: "Total HTTP requests served by the gateway.",
	}, []string{"route", "method", "status"})
	m.httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: ns, Name: "http_request_duration_seconds",
		Help:    "HTTP request latency in seconds.",
		Buckets: latencyBuckets,
	}, []string{"route", "method"})
	m.lifecycle = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "sandbox_lifecycle_total",
		Help: "Sandbox lifecycle operations by backend, operation and result.",
	}, []string{"backend", "operation", "result"})
	m.envdRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "envd_proxy_requests_total",
		Help: "envd data-plane proxy requests by backend and status class.",
	}, []string{"backend", "status_class"})
	m.envdDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: ns, Name: "envd_proxy_duration_seconds",
		Help:    "envd data-plane proxy latency in seconds.",
		Buckets: latencyBuckets,
	}, []string{"backend"})
	m.tokenFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "access_token_validation_failures_total",
		Help: "envd access-token validation failures by backend and reason.",
	}, []string{"backend", "reason"})
	m.warmPoolAcquire = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "warm_pool_acquire_total",
		Help: "Warm pool acquire attempts by backend and result (hit, miss, fallback, bypass).",
	}, []string{"backend", "result"})

	m.reg.MustRegister(m.httpRequests, m.httpDuration, m.lifecycle,
		m.envdRequests, m.envdDuration, m.tokenFailures, m.warmPoolAcquire, m)
	global.Store(m)
	return m
}

// Describe implements prometheus.Collector for the scrape-time gauges.
func (m *Metrics) Describe(ch chan<- *prometheus.Desc) {
	ch <- m.sandboxActive
	ch <- m.backendHealth
}

// Collect implements prometheus.Collector.
func (m *Metrics) Collect(ch chan<- prometheus.Metric) {
	if m.src.SandboxCounts != nil {
		for backend, n := range m.src.SandboxCounts() {
			ch <- prometheus.MustNewConstMetric(m.sandboxActive, prometheus.GaugeValue, float64(n), backend)
		}
	}
	if m.src.BackendHealth != nil {
		for backend, ok := range m.src.BackendHealth() {
			v := 0.0
			if ok {
				v = 1
			}
			ch <- prometheus.MustNewConstMetric(m.backendHealth, prometheus.GaugeValue, v, backend)
		}
	}
}

// Current returns the process-global recorder, or nil when metrics are
// disabled or uninitialized.
func Current() *Metrics { return global.Load() }

// Reset clears the global recorder (tests only).
func Reset() { global.Store(nil) }

// Handler returns the promhttp handler for the global registry, or a 404
// handler when metrics are disabled.
func Handler() http.Handler {
	m := Current()
	if m == nil {
		return http.NotFoundHandler()
	}
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// ObserveHTTP records one served HTTP request (used by the metrics middleware).
func ObserveHTTP(route, method string, status int, dur time.Duration) {
	m := Current()
	if m == nil {
		return
	}
	m.httpRequests.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
	m.httpDuration.WithLabelValues(route, method).Observe(dur.Seconds())
}

// RecordLifecycle records a sandbox lifecycle operation.
func RecordLifecycle(backend, operation, result string) {
	if m := Current(); m != nil {
		m.lifecycle.WithLabelValues(backend, operation, result).Inc()
	}
}

// RecordEnvdProxy records one proxied envd data-plane request.
func RecordEnvdProxy(backend string, status int, dur time.Duration) {
	m := Current()
	if m == nil {
		return
	}
	m.envdRequests.WithLabelValues(backend, statusClass(status)).Inc()
	m.envdDuration.WithLabelValues(backend).Observe(dur.Seconds())
}

// RecordTokenFailure records an envd access-token validation failure.
func RecordTokenFailure(backend, reason string) {
	if m := Current(); m != nil {
		m.tokenFailures.WithLabelValues(backend, reason).Inc()
	}
}

// RecordWarmPoolAcquire records a warm pool acquire outcome.
func RecordWarmPoolAcquire(backend, result string) {
	if m := Current(); m != nil {
		m.warmPoolAcquire.WithLabelValues(backend, result).Inc()
	}
}

func statusClass(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	case status >= 200:
		return "2xx"
	default:
		return "1xx"
	}
}

// sanitizeNamespace maps a configured prefix to a valid Prometheus namespace.
func sanitizeNamespace(p string) string {
	if p == "" {
		return "e2bgw"
	}
	var b strings.Builder
	for i, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			if i == 0 {
				b.WriteRune('_')
			}
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
