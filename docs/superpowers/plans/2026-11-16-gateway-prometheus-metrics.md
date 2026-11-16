# Gateway Prometheus Metrics Implementation Plan (issue #93)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `observability.metrics` 配置真正生效：主端口暴露 Prometheus `/metrics`，覆盖 HTTP、生命周期、envd 代理、token 校验、warm pool、后端健康、活跃 sandbox 指标。

**Architecture:** 新包 `internal/metrics` 持有自建 `prometheus.Registry`（namespace=配置 prefix），通过进程级 `atomic.Pointer` 全局访问器（no-op 安全）供 middleware、handlers、envd proxy、adapters 记录，避免穿透全部工厂签名。gauge 类指标用自定义 Collector 在 scrape 时从 `SandboxBackendMap` 与 `routing.Router.GetHealthStatus()` 现算。

**Tech Stack:** Go 1.26、`github.com/prometheus/client_golang`（新增依赖）、chi v5、现有 config/viper。

**Spec:** `docs/superpowers/specs/2026-11-16-gateway-prometheus-metrics-design.md`

## Global Constraints

- 配置 schema 不变：仅消费现有 `MetricsConfig{Enabled,Path,Prefix}`（`internal/config/config.go:183-187`），默认 `enabled=true, path=/metrics, prefix=e2bgw`（config.go:241-243）。
- `Enabled=false` 时：不注册路由、全局 recorder 为 no-op、不产生任何指标写入。
- 路由 label 防基数爆炸：chi RoutePattern，未匹配归 `"unmatched"`；envd 代理 status 用 class（`2xx/4xx/5xx`），HTTP middleware status 用精确码。
- Histogram buckets 统一：`{.005,.01,.025,.05,.1,.25,.5,1,2.5,5,10,30,60}`。
- 每个 commit 遵循 Conventional Commits + `--signoff`（DCO 必需）。
- 所有测试命令用 `go test ./<pkg>/ -run <Name> -race -count=1`。
- 结束前 `make pre-commit` 必须通过。

---

### Task 1: `internal/metrics` 核心包（registry + 全局访问器 + Record 函数）

**Files:**
- Create: `internal/metrics/metrics.go`
- Test: `internal/metrics/metrics_test.go`

**Interfaces:**
- Consumes: `config.MetricsConfig`（已存在）。
- Produces（后续任务全部依赖这些精确签名）:
  ```go
  func Init(cfg config.MetricsConfig, src Sources) *Metrics   // Enabled=false 时置空全局并返回 nil
  func Current() *Metrics                                      // nil = 禁用/未初始化
  func Reset()                                                 // 测试用，清空全局
  func Handler() http.Handler                                  // promhttp；Current()==nil 时返回 404 handler
  func RecordLifecycle(backend, operation, result string)      // operation: create|delete|pause|resume; result: success|error
  func RecordEnvdProxy(backend string, status int, dur time.Duration)  // status 精确码，内部归 class
  func RecordTokenFailure(backend, reason string)              // reason: missing|invalid
  func RecordWarmPoolAcquire(backend, result string)           // result: hit|miss|fallback|bypass
  func ObserveHTTP(route, method string, status int, dur time.Duration) // 供 middleware 使用
  type Sources struct {
      SandboxCounts func() map[string]int   // backend -> active sandbox count
      BackendHealth func() map[string]bool  // backend -> healthy
  }
  ```

- [ ] **Step 1: 添加依赖**

```bash
go get github.com/prometheus/client_golang@latest
```

确认 go.mod 出现 `github.com/prometheus/client_golang`。

- [ ] **Step 2: 写失败测试** `internal/metrics/metrics_test.go`

```go
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
```

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./internal/metrics/ -race -count=1`
Expected: FAIL（包不存在/未定义符号）

- [ ] **Step 4: 实现 `internal/metrics/metrics.go`**

```go
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

	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/metrics/ -race -count=1`
Expected: PASS（3 个测试）

- [ ] **Step 6: Commit**

```bash
go mod tidy
git add go.mod go.sum internal/metrics/
git commit --signoff -m "feat(metrics): add Prometheus registry, global recorder and core instruments (#93)"
```

---

### Task 2: `SandboxBackendMap.CountByBackend`

**Files:**
- Modify: `internal/adapter/interface.go`（`SandboxBackendMap` 方法区，`Size()` 之后）
- Test: `internal/adapter/interface_test.go`（若无则新建；先确认包内已有测试文件命名风格，跟随）

**Interfaces:**
- Produces: `func (m *SandboxBackendMap) CountByBackend() map[string]int` — Task 5 的 `Sources.SandboxCounts` 使用。

- [ ] **Step 1: 写失败测试**

```go
func TestSandboxBackendMapCountByBackend(t *testing.T) {
	m := adapter.NewSandboxBackendMap()
	m.Set("sb1", "agent-sandbox")
	m.Set("sb2", "agent-sandbox")
	m.Set("sb3", "opensandbox")
	got := m.CountByBackend()
	if got["agent-sandbox"] != 2 || got["opensandbox"] != 1 || len(got) != 2 {
		t.Fatalf("CountByBackend() = %v", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/adapter/ -run TestSandboxBackendMapCountByBackend -race -count=1`
Expected: FAIL（方法未定义）

- [ ] **Step 3: 实现**（加在 `Size()` 方法之后）

```go
// CountByBackend returns the number of tracked sandboxes per backend name.
func (m *SandboxBackendMap) CountByBackend() map[string]int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	counts := make(map[string]int, len(m.mapping))
	for _, backend := range m.mapping {
		counts[backend]++
	}
	return counts
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/adapter/ -run TestSandboxBackendMapCountByBackend -race -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/
git commit --signoff -m "feat(adapter): add SandboxBackendMap.CountByBackend for metrics gauges (#93)"
```

---

### Task 3: HTTP metrics middleware + `/metrics` 路由 + server 接线

**Files:**
- Create: `internal/server/middleware/metrics.go`
- Create: `internal/server/middleware/metrics_test.go`
- Modify: `internal/server/http.go`（`New()` 内初始化 metrics；`buildRouter()` 175-187 区域）
- Modify: `internal/server/server_test.go`（`newTestServer` cfg 补 Metrics；新增 2 个端点测试）

**Interfaces:**
- Consumes: Task 1 的 `metrics.Init/Handler/Current/ObserveHTTP/Reset`、Task 2 的 `CountByBackend`、既有 `routing.Router.GetHealthStatus()`（router.go:328）。
- Produces: `func Metrics(next http.Handler) http.Handler`（middleware 包，风格同 `RequestLogger`）。

- [ ] **Step 1: 写 middleware 失败测试** `internal/server/middleware/metrics_test.go`

```go
package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	h := gwmiddleware.Metrics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	h := gwmiddleware.Metrics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	h := gwmiddleware.Metrics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if !called {
		t.Fatal("next handler must still run when metrics disabled")
	}
}
```

注：`_ = time.Now` 若未用到 time 则删掉 import。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/server/middleware/ -run TestMetrics -race -count=1`
Expected: FAIL（`gwmiddleware.Metrics` 未定义）

- [ ] **Step 3: 实现 `internal/server/middleware/metrics.go`**

```go
package middleware

import (
	"net/http"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/metrics"
	"github.com/go-chi/chi/v5"
)

// Metrics records request counts and latency into the gateway's Prometheus
// registry. It is a no-op when metrics are disabled. The metrics endpoint
// itself is not recorded to avoid self-noise.
func Metrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == metricsPath || metrics.Current() == nil {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		ww := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(ww, r)

		route := "unmatched"
		if rc := chi.RouteContext(r.Context()); rc != nil {
			if p := rc.RoutePattern(); p != "" {
				route = p
			}
		}
		metrics.ObserveHTTP(route, r.Method, ww.status, time.Since(start))
	})
}
```

- [ ] **Step 4: 跑 middleware 测试确认通过**

Run: `go test ./internal/server/middleware/ -run TestMetrics -race -count=1`
Expected: PASS

- [ ] **Step 5: 接线 server**（`internal/server/http.go`）

5a. `New(cfg)` 中，在 `buildRouter()` 调用之前插入（此时 `registry`、`routeMgr` 已构造；若字段名不同以实际代码为准）：

```go
	metrics.Init(cfg.Observability.Metrics, metrics.Sources{
		SandboxCounts: func() map[string]int {
			if sb := registry.SandboxBackend(); sb != nil {
				return sb.CountByBackend()
			}
			return nil
		},
		BackendHealth: func() map[string]bool {
			if routeMgr != nil {
				return routeMgr.GetHealthStatus()
			}
			return nil
		},
	})
```

（import `"github.com/e2bgateway/e2bgateway/internal/metrics"`；闭包捕获的变量名必须与 New 内实际局部变量一致。）

5b. `buildRouter()` middleware 链，`Recovery` 之后插入：

```go
	r.Use(gwmiddleware.Metrics)
```

5c. `buildRouter()` 健康端点之后（catch-all 之前）注册路由：

```go
	// Prometheus metrics endpoint (unauthenticated; exempted in middleware).
	if s.cfg.Observability.Metrics.Enabled {
		mpath := s.cfg.Observability.Metrics.Path
		if mpath == "" {
			mpath = "/metrics"
		}
		r.Get(mpath, metrics.Handler().ServeHTTP)
	}
```

- [ ] **Step 6: 写 server 端点测试**（`internal/server/server_test.go`）

6a. `newTestServer` 的 cfg `Observability` 改为：

```go
		Observability: config.ObservabilityConfig{
			Logging: config.LoggingConfig{Level: "debug"},
			Metrics: config.MetricsConfig{Enabled: true, Path: "/metrics", Prefix: "e2bgw"},
		},
```

6b. 新增测试（同文件）：

```go
func TestMetricsEndpoint(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz") // generate one counted request
	if err != nil {
		t.Fatalf("healthz: %v", err)
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

func TestMetricsEndpointDisabled(t *testing.T) {
	cfg := ... // 复制 newTestServer 的 cfg 构造，但 Metrics.Enabled=false
	// 直接内联构造：与 newTestServer 相同，仅 Observability.Metrics.Enabled=false
	srv, err := server.New(cfg)
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
```

注意：`newTestServer` 为共用 helper，测试间共享全局 recorder 无碍（Init 幂等替换）；如有并行测试冲突，在两个新测试开头调用 `metrics.Reset()` 并 `t.Cleanup(metrics.Reset)` 后重建 server（Init 发生在 server.New 内，Reset 后再 New 即可）。文件顶部补 import：`io`、`strings`。

- [ ] **Step 7: 跑测试确认通过**

Run: `go test ./internal/server/... -race -count=1`
Expected: PASS（含既有测试不回归）

- [ ] **Step 8: Commit**

```bash
git add internal/server/ 
git commit --signoff -m "feat(server): serve Prometheus /metrics and record HTTP request metrics (#93)"
```

---

### Task 4: sandbox 生命周期计数（api/v1 handlers）

**Files:**
- Modify: `internal/api/v1/handlers.go`（Create 39-105、Kill 185-201、Pause 205-218、Resume 221-235）
- Test: `internal/api/v1/handlers_test.go`（追加）

**Interfaces:**
- Consumes: `metrics.RecordLifecycle(backend, operation, result string)`（Task 1）。

- [ ] **Step 1: 写失败测试**（追加到 `handlers_test.go`；沿用该文件既有 mock registry/router 构造方式——先读文件头找到现有 helper，如 `setupTestRegistry`。以下为断言核心，包在既有 helper 之上）

```go
func TestCreateSandboxRecordsLifecycleMetric(t *testing.T) {
	metrics.Reset()
	t.Cleanup(metrics.Reset)
	metrics.Init(config.MetricsConfig{Enabled: true, Path: "/metrics", Prefix: "e2bgw"}, metrics.Sources{})

	// 使用本文件既有的 mock registry/router 构造方式创建 handler 并请求一次成功创建
	// （照抄 TestCreateSandbox 类既有测试的 setup，仅替换断言部分）：
	// ... handler(registry, router, "")(rec, req) ...

	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), `e2bgw_sandbox_lifecycle_total{backend="mock",operation="create",result="success"} 1`) {
		t.Errorf("missing create success counter:\n%s", rec.Body.String())
	}
}
```

再写一个失败路径测试（mock adapter 返回 error 时 `result="error"` 计数 1）。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api/v1/ -run TestCreateSandboxRecords -race -count=1`
Expected: FAIL（无该指标输出）

- [ ] **Step 3: 实现**（handlers.go 四处）

3a. `CreateSandboxHandler`：`a.CreateSandbox` 调用处（约 76 行）：

```go
		sandbox, err := a.CreateSandbox(r.Context(), req)
		if err != nil {
			metrics.RecordLifecycle(backendName, "create", "error")
			writeAdapterError(w, err, http.StatusInternalServerError)
			return
		}
		metrics.RecordLifecycle(backendName, "create", "success")
```

3b. `KillSandboxHandler`（resolve 失败不计数——backend 未知）：

```go
		if err := a.KillSandbox(r.Context(), sandboxID); err != nil {
			metrics.RecordLifecycle(a.Name(), "delete", "error")
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		metrics.RecordLifecycle(a.Name(), "delete", "success")
```

3c. `PauseSandboxHandler`：

```go
		var lastBackend string
		for _, a := range registry.List() {
			lastBackend = a.Name()
			if err := a.PauseSandbox(r.Context(), sandboxID); err == nil {
				metrics.RecordLifecycle(a.Name(), "pause", "success")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		metrics.RecordLifecycle(lastBackend, "pause", "error")
		writeError(w, http.StatusNotFound, ...)
```

3d. `ResumeSandboxHandler`：同 3c 模式，operation=`"resume"`。

import：`"github.com/e2bgateway/e2bgateway/internal/metrics"`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/api/... -race -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api/v1/
git commit --signoff -m "feat(api): count sandbox lifecycle operations per backend (#93)"
```

---

### Task 5: envd 代理指标（请求/延迟/token 失败）

**Files:**
- Modify: `internal/server/envd_proxy.go`（handler 43-128）
- Test: `internal/server/envd_proxy_test.go`（追加；若构造完整 Server 成本高，则将测试放 `server_test.go` 走 `srv.Handler()` 集成路径，仿 Task 3 测试）

**Interfaces:**
- Consumes: `metrics.RecordEnvdProxy(backend string, status int, dur time.Duration)`、`metrics.RecordTokenFailure(backend, reason string)`（Task 1）。

- [ ] **Step 1: 写失败测试**（走集成路径：mock backend + `E2b-Sandbox-Id` header + 无 token → 401）

```go
func TestEnvdProxyRecordsTokenFailure(t *testing.T) {
	srv := newTestServer(t) // metrics enabled via Task 3 changes
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/files", strings.NewReader("{}"))
	req.Header.Set("E2b-Sandbox-Id", "sbx-nonexistent")
	// no X-Access-Token
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()

	mrec := httptest.NewRecorder()
	http.Get(ts.URL + "/metrics") //nolint:errcheck
	_ = mrec
	// scrape via the server itself:
	resp2, _ := http.Get(ts.URL + "/metrics")
	body, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if !strings.Contains(string(body), `e2bgw_access_token_validation_failures_total{backend="mock",reason="missing"}`) {
		t.Errorf("missing token failure metric:\n%s", body)
	}
}
```

注意：请求必须打到 catch-all（任意非注册路径如 `/files`）。若 mock adapter 的 `ValidateAccessToken` 行为导致走 `invalid` 分支，则断言 `reason="invalid"`——先跑一次看实际值再固定断言。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/server/ -run TestEnvdProxyRecords -race -count=1`
Expected: FAIL

- [ ] **Step 3: 实现**（envd_proxy.go）

3a. 文件顶部加私有 status 捕获器（middleware 的 `statusWriter` 不可见）：

```go
// proxyStatusWriter captures the upstream response status for metrics.
type proxyStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *proxyStatusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *proxyStatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
```

3b. handler 中：backend 名在两个分支收敛为单一变量 `backendName`（现有代码在 if/else 内各自声明——把 `backendName` 提到分支外）：

```go
		var a adapter.SandboxAdapter
		var ok bool
		backendName := ""

		if s.registry.SandboxBackend() != nil {
			if name, found := s.registry.SandboxBackend().Get(sandboxID); found {
				backendName = name
				a, ok = s.registry.Get(name)
			}
		}
		if !ok {
			name, err := s.routeMgr.SelectBackend(r.Context(), &routing.RoutingRequest{})
			...
			backendName = name
			a, ok = s.registry.Get(name)
			...
		}
```

3c. token 失败两处：

```go
		if token == "" {
			metrics.RecordTokenFailure(backendName, "missing")
			http.Error(...)
			return
		}
		valid, err := a.ValidateAccessToken(r.Context(), sandboxID, token)
		if err != nil || !valid {
			metrics.RecordTokenFailure(backendName, "invalid")
			http.Error(...)
			return
		}
```

3d. 代理转发计时：

```go
		proxyStart := time.Now()
		ww := &proxyStatusWriter{ResponseWriter: w, status: http.StatusOK}
		proxy.ServeHTTP(ww, r)
		metrics.RecordEnvdProxy(backendName, ww.status, time.Since(proxyStart))
```

import：`time`、`internal/metrics`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/server/ -race -count=1`
Expected: PASS（全部 server 测试无回归）

- [ ] **Step 5: Commit**

```bash
git add internal/server/
git commit --signoff -m "feat(envd): record proxy request, latency and token-failure metrics (#93)"
```

---

### Task 6: warm pool acquire 计数（两个 adapter）

**Files:**
- Modify: `internal/adapter/opensandbox/adapter.go`（CreateSandbox 372-405 区域）
- Modify: `internal/adapter/agentsandbox/adapter.go`（CreateSandbox 276-300 区域）
- Test: `internal/adapter/opensandbox/`、`internal/adapter/agentsandbox/` 现有单测文件中追加（先找到覆盖 CreateSandbox pool 路径的既有测试，跟随其构造方式）

**Interfaces:**
- Consumes: `metrics.RecordWarmPoolAcquire(backend, result string)`（Task 1）。result 值域：`hit|miss|fallback|bypass`。

- [ ] **Step 1: 写失败测试**

opensandbox：在现有 pool 相关测试文件中（搜 `lookupPoolByTemplate`/`AcquireID` 的测试），对已有「池命中」用例追加断言：Init metrics → 调 CreateSandbox → scrape 含 `e2bgw_warm_pool_acquire_total{backend="<name>",result="hit"}`。对「envs 绕过」用例断言 `result="bypass"`。若既有测试基建不便，可新建最小用例（mock pool via `newPoolFn`）。

agentsandbox：`resolveWarmPool` 返回非空时经 mock client 创建成功后断言 `result="hit"`；返回空时 `result="miss"`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/adapter/opensandbox/ ./internal/adapter/agentsandbox/ -run WarmPool -race -count=1`
Expected: FAIL

- [ ] **Step 3: 实现**

3a. opensandbox `CreateSandbox`（382-403 区域）：

```go
	if len(req.Envs) == 0 && len(req.Metadata) == 0 {
		if entry := a.lookupPoolByTemplate(req.TemplateID); entry != nil {
			sandboxID, acqErr := entry.pool.AcquireID(ctx)
			if acqErr == nil {
				metrics.RecordWarmPoolAcquire(a.name, "hit")
				...原成功返回...
			}
			metrics.RecordWarmPoolAcquire(a.name, "fallback")
			slog.Warn(...)
		} else {
			metrics.RecordWarmPoolAcquire(a.name, "miss")
		}
	} else {
		metrics.RecordWarmPoolAcquire(a.name, "bypass")
	}
```

3b. agentsandbox `CreateSandbox`（276-300 区域）：

```go
	warmPool := a.resolveWarmPool(ctx, req.TemplateID)
	if len(req.Envs) > 0 {
		metrics.RecordWarmPoolAcquire(a.name, "bypass")
		...原 sandbox.New 路径...
	} else {
		if warmPool != "" {
			metrics.RecordWarmPoolAcquire(a.name, "hit")
		} else {
			metrics.RecordWarmPoolAcquire(a.name, "miss")
		}
		sb, err = a.client.CreateSandbox(ctx, warmPool, a.namespace)
		...
	}
```

注：agentsandbox 的 hit/miss 表示「是否解析到 warm pool 路由」（真正命中由 k8s controller 决定），与 spec 一致。字段名 `a.name` 以实际 struct 字段为准。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/adapter/... -race -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/
git commit --signoff -m "feat(adapter): count warm pool acquire outcomes per backend (#93)"
```

---

### Task 7: 文档与 deploy 同步

**Files:**
- Modify: `deploy/helm/e2bgateway/values.yaml`（metrics 注释 + podAnnotations 抓取示例）
- Modify: `deploy/helm/e2bgateway/templates/NOTES.txt`（恢复 📊 Metrics 段）
- Modify: `deploy/helm/e2bgateway/README.md`（Parameters 补 metrics 三键；新增 Monitoring 小节）
- Modify: `deploy/kustomize/base/configmap.yaml`（metrics 段注释）
- Modify: `configs/e2bgateway-example.yaml`（metrics 段注释）
- Modify: `AGENTS.md`（observability 一句）
- Modify: `CHANGELOG.md`（0.1.0 节后新增 Unreleased？——否：仓库当前无 Unreleased 惯例冲突，加 `## Unreleased` 段一条 `[FEATURE]`）

**Interfaces:** 无代码接口；纯文档。

- [ ] **Step 1: helm values.yaml**

`config.observability.metrics` 段改为（保留现有键值）：

```yaml
    # Gateway self-metrics (Prometheus, pull-based). Served on the main HTTP
    # port — there is no separate metrics port.
    metrics:
      enabled: true
      # NOTE: auth/rate-limit/audit exemptions match the literal "/metrics"
      # path; keep the default unless you also adjust middleware constants.
      path: /metrics
      # Prometheus namespace for all metrics, e.g. e2bgw_http_requests_total.
      prefix: e2bgw
```

`podAnnotations` 处：

```yaml
# Prometheus scrape annotations (if your cluster discovers pods by annotation):
# podAnnotations:
#   prometheus.io/scrape: "true"
#   prometheus.io/port: "8080"
#   prometheus.io/path: "/metrics"
podAnnotations: {}
```

- [ ] **Step 2: helm NOTES.txt** — 在 `🔐 Authentication` 段之前插入：

```
📊 Metrics
-------------------------------------------------------------

{{- if .Values.config.observability.metrics.enabled }}
Prometheus endpoint: http://<pod-ip>:{{ .Values.service.httpPort }}{{ .Values.config.observability.metrics.path | default "/metrics" }}
(served on the main HTTP port; no auth required)
{{- else }}
Metrics are DISABLED (config.observability.metrics.enabled=false)
{{- end }}
```

- [ ] **Step 3: helm README** — Parameters 表补三行（`config.observability.metrics.enabled/path/prefix`）；新增 `## Monitoring` 小节：指标清单表（9 个指标族，照 spec 表格）、pod-annotation 抓取示例、kube-prometheus-stack ServiceMonitor 片段（port: http, path: /metrics）。

- [ ] **Step 4: kustomize configmap + example yaml** — `observability.metrics` 段加与 Step 1 相同注释（YAML 内 `#`）。

- [ ] **Step 5: AGENTS.md** — observability 相关小节补一句：网关自身指标经 `internal/metrics` 暴露于主端口 `observability.metrics.path`，指标清单见 helm README Monitoring 节。

- [ ] **Step 6: CHANGELOG.md** — 文件顶部（`## 0.1.0` 之前、前言之后）加：

```markdown
## Unreleased

* [FEATURE] Serve Prometheus metrics on the main HTTP port (`observability.metrics`): HTTP request/latency, sandbox lifecycle, envd proxy, access-token failures, warm pool acquire, backend health and active sandbox gauges. #93
```

- [ ] **Step 7: 验证渲染**

Run: `helm lint deploy/helm/e2bgateway && helm template t deploy/helm/e2bgateway >/dev/null && kubectl kustomize deploy/kustomize/base >/dev/null`
Expected: 全部成功

- [ ] **Step 8: Commit**

```bash
git add deploy/ configs/ AGENTS.md CHANGELOG.md
git commit --signoff -m "docs: document gateway Prometheus metrics in helm, kustomize and guides (#93)"
```

---

### Task 8: 全量验证 + 手工冒烟

- [ ] **Step 1: `make pre-commit`** — Expected: exit 0
- [ ] **Step 2: 冒烟**：用 mock backend 配置本地起服务并 scrape：

```bash
go run ./cmd/e2bgateway serve --config configs/e2bgateway-test.yaml &
curl -s localhost:<port>/metrics | head -20
curl -s -X POST localhost:<port>/sandboxes -d '{"templateID":"x"}' -H 'Content-Type: application/json' >/dev/null
curl -s localhost:<port>/metrics | grep -E 'lifecycle|sandbox_active'
kill %1
```

Expected: `/metrics` 200；create 后 `sandbox_lifecycle_total{...operation="create"...}` 出现（mock backend 成功则 result="success"）。configs/e2bgateway-test.yaml 若无 mock 后端则改用 test/ 下现成 mock 配置。

- [ ] **Step 3: 推送分支**

```bash
git push -u origin add-metrics
```

---

## Self-Review 结果

- **Spec 覆盖**：9 个指标族 → Task 1（7 个 instrument 定义）+ Task 2/3（2 个 gauge 数据源与注册）；路由/禁用语义 → Task 3；防基数（route pattern/status class/buckets）→ Task 1/3/5；生命周期计一次语义 → Task 4；warm pool 四类 result → Task 6；文档+helm/config 同步（用户追加要求）→ Task 7；验收标准 1-5 → Task 3/8。无缺口。
- **占位符**：Task 4/6 测试步骤指示「沿用既有 helper」属探索性指引，因 handlers_test/adapter 测试的既有构造方式需实现者现场读取（计划内已给出断言核心与精确 grep 目标）；其余步骤均含完整代码。
- **类型一致性**：`ObserveHTTP(route, method string, status int, dur time.Duration)`、`RecordEnvdProxy(backend string, status int, dur time.Duration)`、`Sources{SandboxCounts, BackendHealth}`、`CountByBackend() map[string]int` 在 Task 1/2/3/5 中签名一致。
