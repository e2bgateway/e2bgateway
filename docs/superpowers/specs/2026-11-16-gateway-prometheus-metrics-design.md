# Design: Gateway Prometheus Metrics (issue #93)

日期：2026-11-16
分支：`add-metrics`（基于 main `f3deced`）
Issue：https://github.com/e2bgateway/e2bgateway/issues/93

## 背景

`observability.metrics` 配置面（enabled/path/prefix，默认 true、`/metrics`、`e2bgw`）
完全没有消费者：无 `/metrics` 路由（请求落入 envd catch-all 代理返回 400）、无
Prometheus handler、无任何 instrument；OTel MeterProvider 初始化后也从不被写入。
网关自身零可观测性。本设计实现 pull 型 Prometheus 指标（OTLP 空导出问题不在本
issue 范围）。

## 决策（用户确认）

- **实现路径**：`prometheus/client_golang` 直连 + `promhttp`，挂主 HTTP 端口。
  不走 OTel 桥接。
- **指标范围**：完整集（issue #93 提案的全部 instrument）。

## 架构

### 新包 `internal/metrics`

- `Metrics` struct 持有自建 `prometheus.Registry`（不用全局 default registry，
  保证测试隔离与可禁用），namespace = `cfg.Prefix`（默认 `e2bgw`）。
- **包级访问器**：`metrics.RecordLifecycle(backend, op, result)`、
  `metrics.RecordEnvdProxy(backend, statusClass, dur)`、
  `metrics.RecordTokenFailure(backend, reason)`、
  `metrics.RecordWarmPoolAcquire(backend, result)` 等，写入一个进程级全局实例
  （`atomic.Pointer[Metrics]`）。未初始化或 `Enabled=false` 时全部为 no-op。
  选择全局访问器而非显式参数穿透的原因：注入点横跨 api/v1 handler 工厂、
  envd proxy、两个 adapter 工厂，显式注入需要改动全部工厂签名与既有测试，
  改动面与收益不成比例；网关是单进程服务，全局 recorder 是 Prometheus 生态
  的惯用形态（client_golang 默认 registry 本身即全局）。
- `Init(cfg config.MetricsConfig) *Metrics`（由 `server.New` 调用）/
  `Reset()`（测试用）/ `Handler() http.Handler`（promhttp）。

### 指标集合

| 指标（namespace 前缀后） | 类型 | Labels | 注入点 |
|---|---|---|---|
| `http_requests_total` | CounterVec | route, method, status | 新 middleware `internal/server/middleware/metrics.go` |
| `http_request_duration_seconds` | HistogramVec | route, method | 同上 |
| `sandbox_lifecycle_total` | CounterVec | backend, operation, result | `internal/api/v1/handlers.go`：Create(53/76)、Kill(185-201)、Pause(205-218)、Resume(221-235) |
| `sandbox_active` | Gauge（自定义 Collector） | backend | scrape 时读 `registry.SandboxBackend()` 映射计数 |
| `backend_health` | Gauge（自定义 Collector） | backend | scrape 时读 `routing.Router.GetHealthStatus()`（router.go:328） |
| `envd_proxy_requests_total` | CounterVec | backend, status_class | `internal/server/envd_proxy.go`（响应 status 归 2xx/4xx/5xx） |
| `envd_proxy_duration_seconds` | HistogramVec | backend | 同上（包裹 `proxy.ServeHTTP`，envd_proxy.go:126） |
| `access_token_validation_failures_total` | CounterVec | backend, reason | envd_proxy.go:81-84（missing）、86-89（invalid/error） |
| `warm_pool_acquire_total` | CounterVec | backend, result | opensandbox adapter.go:383-403（hit/fallback/bypass）；agentsandbox adapter.go:277/296（hit=解析到 warmPool 名 / miss=空 / bypass=envs 冷启动） |

### 路由注册

- `buildRouter()` 在 `/healthz`、`/readyz` 旁注册
  `r.Get(cfgPath, promhttp handler)`（http.go:186-187 处），**必须先于
  catch-all（http.go:378）**。
- Path 取 `s.cfg.Observability.Metrics.Path`；为空时回落 `/metrics`。
- Auth/RateLimit/AuditLog 对 `/metrics` 的豁免已存在（middleware.go:121/161/192），
  不改。豁免按常量 `/metrics` 判断：当配置了非默认 Path 时豁免不生效——接受此
  限制（与现有代码一致），在配置注释中说明建议使用默认路径。
- `Enabled=false`：不注册路由（`/metrics` 维持现状落入代理→400），全局
  recorder 保持 no-op，启动时打一条 info 日志。

### HTTP middleware

- 仿 `RequestLogger`（middleware.go:61）+ `statusWriter`（226）实现：计时、
  捕获 status、请求结束后一次落库。
- route label：`chi.RouteContext(r.Context()).RoutePattern()`；空/未匹配归
  `"unmatched"`，防 URL 扫描造成基数爆炸。
- 跳过 metrics 路径自身（避免自噪声）。
- 挂在 middleware 链 `Recovery` 之后（http.go:175-183），使 panic→500 也被计数。

### 依赖与配置

- go.mod 新增 `github.com/prometheus/client_golang`（当前无任何 prometheus 依赖）。
- `MetricsConfig` schema 不变，现有配置零迁移；三个键全部真正生效。

### 边界与错误处理

- pause/resume 为遍历 adapter 尝试：只在**成功或全部失败**时计一次，
  backend label 取实际成功的 adapter（全失败时取最后一个尝试的）。
- status label：HTTP middleware 用精确码（值域有限）；envd 代理用 class
  （透传任意上游状态码，防基数）。
- Histogram buckets：HTTP 与 envd 代理均用
  `{.005,.01,.025,.05,.1,.25,.5,1,2.5,5,10,30,60}`（覆盖长命令场景）。
- Collector 读取映射时持相应锁（`SandboxBackend()` 内部为并发安全 map；
  `GetHealthStatus()` 自带 RWMutex）。

## 测试（对齐现有风格）

- `internal/metrics/metrics_test.go`：Init/Reset/no-op 行为；各 Record 函数
  产出正确样本（`prometheus/testutil.CollectAndCompare` / `ToFloat64`）；
  Collector 输出。
- `internal/server/middleware/metrics_test.go`：表驱动，仿
  `TestAuthMiddleware_SkipsHealth` 风格；status 捕获、route 归一化、
  metrics 路径跳过。
- `internal/server/server_test.go`：`newTestServer` 的 cfg 补
  `Observability.Metrics{Enabled:true, Path:"/metrics", Prefix:"e2bgw"}`；
  新增 `TestMetricsEndpoint`（200、body 含 `e2bgw_http_requests_total`）与
  `TestMetricsEndpointDisabled`（未注册）。
- `internal/api/v1/handlers_test.go`：create 成功/失败、kill 的 counter 断言。
- `internal/server/envd_proxy_test.go`：token missing/invalid 计数（现有文件
  只有纯函数测试，新增需要构造最小 Server/registry——若成本过高则放
  server_test.go 走集成路径）。
- warm pool 计数：在两个 adapter 的现有单测中补断言（opensandbox
  AcquireID hit/fallback；agentsandbox resolveWarmPool 命中/未命中）。

## 文档与 deploy 同步（含 helm config 配置）

### helm chart（`deploy/helm/e2bgateway/`）

- `values.yaml`：
  - `config.observability.metrics` 段保留并加注释：三键语义
    （enabled=开关、path=暴露路径、prefix=指标 namespace）、path 建议使用
    默认 `/metrics`（鉴权/限流/审计豁免按常量匹配）。
  - `podAnnotations` 处加注释示例：Prometheus 自动抓取注解
    （`prometheus.io/scrape: "true"`、`prometheus.io/port: "8080"`、
    `prometheus.io/path: "/metrics"`）。
- `templates/NOTES.txt`：恢复 `📊 Metrics` 段 —— endpoint 为
  `http://<pod>:8080{path}`（主端口，无独立 metrics 端口），并注明
  enabled 状态。
- `README.md`：
  - Parameters 表补 `config.observability.metrics.*` 三键与
    `podAnnotations` 抓取注解示例。
  - 新增 “Monitoring” 小节：指标清单（本 spec 表格）、ServiceMonitor/
    pod-annotation 两种抓取方式示例、单副本约束下 scrape 无需特殊处理。

### kustomize（`deploy/kustomize/base/configmap.yaml`）

- `observability.metrics` 段加同样语义注释（enabled/path/prefix，
  path 建议默认值）。

### 其他

- `configs/e2bgateway-example.yaml`：metrics 段加注释（同上）。
- `AGENTS.md`：observability 一节补充 metrics 已实现与指标清单。
- deploy manifests 结构无需改动（#91 已删 9090；指标走主端口）。

## 验收标准

1. `GET /metrics`（默认配置）返回 200，text/plain 版本 0.0.4，含上表全部
   指标族（至少 HTTP 与 gauge 类）。
2. `observability.metrics.enabled: false` 时不注册路由，进程内无指标写入。
3. 自定义 `prefix` 生效为指标 namespace；自定义 `path` 生效为路由。
4. 一次 sandbox create/kill 流量后，`sandbox_lifecycle_total` 与
   `sandbox_active` 数值正确。
5. `make pre-commit` 通过；e2e workflow 不受影响。
