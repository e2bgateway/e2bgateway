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

// backendLabel is the common label name identifying the backend adapter on
// every gateway metric family.
const backendLabel = "backend"

// resultLabel is the common label name for operation outcomes.
const resultLabel = "result"

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
	tlsReloads      *prometheus.CounterVec

	cacheL1Hits        *prometheus.CounterVec
	cacheL2Hits        *prometheus.CounterVec
	cacheMisses        *prometheus.CounterVec
	cacheInvalidations *prometheus.CounterVec
	cacheL2Errors      *prometheus.CounterVec

	tlsCertExpiry *prometheus.Desc
	sandboxActive *prometheus.Desc
	backendHealth *prometheus.Desc
	src           Sources

	// tlsNotAfter is the expiry time of the served TLS certificate, or nil
	// until one is loaded (HTTPS disabled), in which case the gauge is
	// omitted rather than reported as a bogus timestamp.
	tlsNotAfter atomic.Pointer[time.Time]
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
		sandboxActive: prometheus.NewDesc(ns+"_sandbox_active", "Active sandboxes tracked by the gateway, per backend.", []string{backendLabel}, nil),
		backendHealth: prometheus.NewDesc(ns+"_backend_health", "Backend health as observed by the routing health checker (1 healthy, 0 unhealthy).", []string{backendLabel}, nil),
		tlsCertExpiry: prometheus.NewDesc(ns+"_tls_certificate_expiry_timestamp_seconds", "Expiry time (NotAfter) of the served TLS certificate as a Unix timestamp. Absent when HTTPS is not configured.", nil, nil),
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
	}, []string{backendLabel, "operation", resultLabel})
	m.envdRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "envd_proxy_requests_total",
		Help: "envd data-plane proxy requests by backend and status class.",
	}, []string{backendLabel, "status_class"})
	m.envdDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: ns, Name: "envd_proxy_duration_seconds",
		Help:    "envd data-plane proxy latency in seconds.",
		Buckets: latencyBuckets,
	}, []string{backendLabel})
	m.tokenFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "access_token_validation_failures_total",
		Help: "envd access-token validation failures by backend and reason.",
	}, []string{backendLabel, "reason"})
	m.warmPoolAcquire = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "warm_pool_acquire_total",
		Help: "Warm pool acquire attempts by backend and result (hit, miss, fallback, bypass).",
	}, []string{backendLabel, resultLabel})
	m.tlsReloads = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "tls_certificate_reloads_total",
		Help: "TLS certificate hot reload attempts by result (success, failure). The initial startup load is not counted.",
	}, []string{resultLabel})
	m.cacheL1Hits = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "cache_l1_hits_total",
		Help: "Tiered cache L1 (in-memory) hits by cache instance.",
	}, []string{"cache"})
	m.cacheL2Hits = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "cache_l2_hits_total",
		Help: "Tiered cache L2 (Redis) hits by cache instance.",
	}, []string{"cache"})
	m.cacheMisses = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "cache_misses_total",
		Help: "Tiered cache misses that triggered a backend load, by cache instance.",
	}, []string{"cache"})
	m.cacheInvalidations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "cache_invalidations_total",
		Help: "Remote invalidation broadcasts applied to L1, by cache instance.",
	}, []string{"cache"})
	m.cacheL2Errors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "cache_l2_errors_total",
		Help: "L2 (Redis) operation failures causing degradation, by cache instance.",
	}, []string{"cache"})

	m.reg.MustRegister(m.httpRequests, m.httpDuration, m.lifecycle,
		m.envdRequests, m.envdDuration, m.tokenFailures, m.warmPoolAcquire,
		m.tlsReloads, m.cacheL1Hits, m.cacheL2Hits, m.cacheMisses,
		m.cacheInvalidations, m.cacheL2Errors, m)
	global.Store(m)
	return m
}

// Describe implements prometheus.Collector for the scrape-time gauges.
func (m *Metrics) Describe(ch chan<- *prometheus.Desc) {
	ch <- m.sandboxActive
	ch <- m.backendHealth
	ch <- m.tlsCertExpiry
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
	if notAfter := m.tlsNotAfter.Load(); notAfter != nil {
		ch <- prometheus.MustNewConstMetric(m.tlsCertExpiry, prometheus.GaugeValue, float64(notAfter.Unix()))
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
	method = normalizeMethod(method)
	m.httpRequests.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
	m.httpDuration.WithLabelValues(route, method).Observe(dur.Seconds())
}

// normalizeMethod bounds the cardinality of the method label. The metrics
// middleware runs before authentication, so an unauthenticated client could
// otherwise create unbounded label series by sending arbitrary methods
// (CWE-400 memory exhaustion). Non-standard methods collapse to "OTHER".
func normalizeMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions,
		http.MethodTrace, http.MethodConnect:
		return method
	default:
		return "OTHER"
	}
}

// RecordLifecycle records a sandbox lifecycle operation.
func RecordLifecycle(backend, operation, result string) {
	if m := Current(); m != nil {
		m.lifecycle.WithLabelValues(backend, operation, result).Inc()
	}
}

// RecordCache records one tiered-cache event. name is the cache instance
// label (sandbox_metadata, template_list); event is one of l1_hit, l2_hit,
// miss, invalidation, l2_error. Unknown events are ignored.
func RecordCache(name, event string) {
	m := Current()
	if m == nil {
		return
	}
	switch event {
	case "l1_hit":
		m.cacheL1Hits.WithLabelValues(name).Inc()
	case "l2_hit":
		m.cacheL2Hits.WithLabelValues(name).Inc()
	case "miss":
		m.cacheMisses.WithLabelValues(name).Inc()
	case "invalidation":
		m.cacheInvalidations.WithLabelValues(name).Inc()
	case "l2_error":
		m.cacheL2Errors.WithLabelValues(name).Inc()
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

// RecordTLSReload records a TLS certificate hot reload attempt by result
// (success, failure). The initial startup load is not a hot reload and must
// not be recorded.
func RecordTLSReload(result string) {
	if m := Current(); m != nil {
		m.tlsReloads.WithLabelValues(result).Inc()
	}
}

// SetTLSCertificateExpiry records the expiry time (NotAfter) of the TLS
// certificate now being served. Exporting the timestamp rather than counting
// warnings lets alerts use any threshold and keep firing until the
// certificate is replaced.
func SetTLSCertificateExpiry(notAfter time.Time) {
	if m := Current(); m != nil {
		m.tlsNotAfter.Store(&notAfter)
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
