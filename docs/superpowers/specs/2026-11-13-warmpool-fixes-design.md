# Warm Pool 修复：PR #82 遗留评审意见 + 强制删除

**Issue**: #80 — [功能增强] Warm Pool 强制删除（DELETE /warm-pools/{id}?force=true）
**Date**: 2026-11-13
**Priority**: P1
**Approach**: A — 逐条修复 PR #82 未解决评审意见（A1-A4）+ 扩展 `DeleteWarmPool(ctx, id, opts)` 接口实现 force 语义

---

## 1. Problem Statement

两块独立但同域（warm pool）的工作合并到一个 branch：

**A. PR #82 合并后仍有 4 条未解决的 coderabbit 评审意见：**

| # | 位置 | 问题 | 级别 |
|---|---|---|---|
| A1 | `internal/adapter/agentsandbox/adapter.go` (`resolveWarmPool`) | 路由刷新（`List`）失败后 `warmPoolRoutingAt` 不更新，后续每个拿到 `warmPoolOpsMu` 的 `CreateSandbox` 都同步重试，低并发下每次最长白等 10s | Major / quick win |
| A2 | `internal/adapter/agentsandbox/warmpool.go` (`DeleteWarmPool`) | CRD 删除成功但随后的 `refreshWarmPoolRouting` 失败时，缓存路由仍指向已删池，claim 报 `WarmPoolNotFound` | Minor / quick win |
| A3 | `internal/adapter/opensandbox/warmpool.go` (`buildPoolStateStore`) | Redis 带密码但走明文 TCP（CWE-319）。`templateStore` 的 Redis（`redis_store.go`）有同样问题 | Major security / quick win |
| A4 | `internal/adapter/opensandbox/warmpool.go` (`defaultNewPool`) | 网关重启后 Redis 残留的 idle 条目不绑定镜像：同 pool ID 换了 template→image 映射后，`CreateSandbox` 可能 acquire 到旧镜像沙箱却报告新池的 template | Major / heavy lift |

**B. Issue #80：warm pool 强制删除。** 当前 `DELETE /api/v1/warm-pools/{warmPoolID}` 只有优雅语义（opensandbox `Shutdown(graceful=true)` 等待 in-flight；agentsandbox 删 CRD 由 controller 按 `updateStrategy` 异步回收）。需要 `?force=true` 提供立即释放语义。

## 2. Design Goals

- A1：刷新失败后退避，避免热路径反复为失败的重试付 10s 超时。
- A2：删除成功后缓存路由立即不再指向被删池，即使 refresh 失败。
- A3：pool stateStore 与 templateStore 两处 Redis 都支持 TLS；`password != ""` 且非 loopback 时**强制** TLS，否则 adapter 构造失败。
- A4：持久化 idle 状态按镜像指纹隔离 namespace；同池同镜像保留跨重启复用，镜像变更则旧条目不可达（随 IdleTimeout 服务端过期）。
- #80：`DeleteWarmPool` 接口扩展为 opts 结构体；opensandbox / agentsandbox 实现 force 语义；handler 解析 query param；OpenAPI 更新；graceful / force 双路径单测。

**Non-goals（本期不做）：**

- e2bcloud 上游 API 无 force 参数，adapter 仅改签名、忽略 opts。
- Redis 自定义 CA / mTLS / `insecureSkipVerify` 配置项（系统 CA 校验即可，需求出现再加）。
- 指数退避（固定退避常量足够）。
- agentsandbox force 删除 claimed 沙箱（绝不触碰 SandboxClaim 拥有的沙箱）。

## 3. Architecture

### 3.1 Interface changes

`internal/adapter/interface.go`：

```go
// WarmPoolDeleteOptions controls DeleteWarmPool teardown semantics.
// A nil *WarmPoolDeleteOptions is equivalent to &WarmPoolDeleteOptions{}
// (graceful).
type WarmPoolDeleteOptions struct {
    // Force requests immediate teardown: opensandbox skips the in-flight
    // drain (Shutdown graceful=false); agentsandbox additionally deletes
    // unclaimed pool sandboxes directly instead of waiting for controller GC.
    Force bool
}

// SandboxAdapter 接口签名变更：
DeleteWarmPool(ctx context.Context, warmPoolID string, opts *WarmPoolDeleteOptions) error
```

**所有实现方同步更新**：`agentsandbox`、`opensandbox`、`e2bcloud`、`mock`（`internal/adapter/mock/adapter.go`）、`test/mock/backend.go`（`DeleteWarmPoolFn` 签名 + record 带 opts）。

### 3.2 Adapter behavior

**opensandbox `DeleteWarmPool(ctx, id, opts)`**（`warmpool.go`）：

- `force := opts != nil && opts.Force`
- `entry.pool.Shutdown(ctx, !force)` —— SDK 已核实（pool.go:755-818）：非优雅 Shutdown 立即停 reconciler、释放 primary lock、不等 in-flight work。
- 之后**仍然**执行 `ReleaseAllIdleParallel(ctx, poolReleaseWorkers)`：非优雅 Shutdown 不清 idle 队列，不 drain 的话 idle 沙箱要等 IdleTimeout 才过期，且共享 Redis state store 下重建同 ID 池可能 acquire 到旧沙箱。
- 其余（pools/poolStarting 登记、opMu 串行、错误包装）不变。

**agentsandbox `DeleteWarmPool(ctx, id, opts)`**（`warmpool.go`）：

- graceful 路径 CRD 删除保持现状（空 `DeleteOptions`）。**Q1 已核实**：claim 认领会重置 Sandbox 的 ownerRef 改指 SandboxClaim（`sandboxclaim_controller.go:1498-1499`），claimed 沙箱不受 WarmPool CRD 的 GC 影响；但 Background propagation 会立即级联删除 pool-owned 的 unclaimed 沙箱，改变 graceful 现行为（CR 默认 orphan + controller 按 `updateStrategy` 回收），故仅 force 使用。
- force 时 CRD 删除使用 `metav1.DeleteOptions{PropagationPolicy: &metav1.DeletePropagationBackground}`，成功后额外执行 best-effort 直接回收：
  1. 按 label `agents.x-k8s.io/warm-pool-sandbox == controllers.NameHash(warmPoolID)` List 池中 Sandbox（`NameHash` 由 `sigs.k8s.io/agent-sandbox/controllers` 导出，与 warmpool controller 打标逻辑一致）。
  2. 只删 **unclaimed**：controller ownerRef 为该 SandboxWarmPool，或 ownerRef 为 nil（孤儿）。ownerRef 是 SandboxClaim（或其他 controller）的一律跳过——claim controller 认领会改 ownerRef（见 `filterActiveSandboxes` 的 `controllerRef.UID == warmPool.UID` 判定）。
  3. 单个 Sandbox 删除失败仅 `slog.Warn`，不影响整体返回成功（Background propagation GC 兜底）。
- A2 修复（见下）与 force 路径共用：CRD 删除成功后先清缓存路由，再 refresh。
- 删除后照旧 `refreshWarmPoolRouting(ctx)`。

**e2bcloud**：签名变更，忽略 opts，行为不变。
**mock（两处）**：签名变更；`test/mock` 记录 opts 供断言。

### 3.3 A1 — 路由刷新退避（agentsandbox）

`Adapter` 新增字段：

```go
// warmPoolRoutingLastAttempt is the start of the most recent routing refresh
// attempt, successful or not. Guarded by warmPoolMapMu.
warmPoolRoutingLastAttempt time.Time
```

新常量：

```go
// warmPoolRoutingRetryBackoff spaces out routing refresh retries after a
// failed attempt so a persistently failing List cannot make every
// CreateSandbox pay the restore timeout synchronously.
const warmPoolRoutingRetryBackoff = 30 * time.Second
```

- `refreshWarmPoolRouting` 入口（或出口）无论成败都在 `warmPoolMapMu` 下写 `warmPoolRoutingLastAttempt = time.Now()`；成功时照旧更新 `warmPoolRoutingAt`。
- `resolveWarmPool` stale 判定改为：

```go
stale := time.Since(a.warmPoolRoutingAt) > warmPoolRoutingTTL &&
    time.Since(a.warmPoolRoutingLastAttempt) >= warmPoolRoutingRetryBackoff
```

（两个时间戳都在同一次 `RLock` 内读。）

### 3.4 A2 — 删除后先清缓存路由（agentsandbox）

`DeleteWarmPool` 中 CRD `Delete` 成功后、`refreshWarmPoolRouting` 之前：

```go
// Drop cached routes to the deleted pool BEFORE the best-effort refresh: if
// the List fails, the cache must not keep pointing claims at a pool that no
// longer exists.
a.warmPoolMapMu.Lock()
for tmpl, wp := range a.warmPoolMap {
    if wp == warmPoolID {
        delete(a.warmPoolMap, tmpl)
    }
}
a.warmPoolMapMu.Unlock()
```

config 覆盖项（`warmPoolConfig`）不在此处恢复——随后的 refresh 成功时会重建；refresh 失败时该 template 回落到 raw template ID，与"池不存在"的语义一致。已持 `warmPoolOpsMu`，与并发 create 的发布不会交错。

### 3.5 A3 — Redis TLS（opensandbox，两处 store）

配置解析（`factory.go`，两个 store 共用；注意 Viper 小写化，`lookupBool`/`lookupString` 已处理双写法）：

- `tls: true|false`（新 key，`lookupBool(storeCfg, "tls")`）。
- `addr` 以 `rediss://` 开头 → 隐式启用 TLS，并剥掉 scheme 只留 host:port。

新共享 helper（opensandbox 包内，如 `redis_tls.go`）：

```go
// redisTLSConfig builds the TLS configuration for a Redis connection.
// Enforcement: a password-bearing connection to a non-loopback address must
// use TLS (CWE-319); otherwise adapter construction fails.
func redisTLSConfig(addr string, tlsEnabled, hasPassword bool) (*tls.Config, error)
```

规则：

| 条件 | 结果 |
|---|---|
| `tlsEnabled`（含 `rediss://` 隐式） | 返回 `&tls.Config{ServerName: <addr 的 host>}`（系统 CA 校验） |
| `password != ""` 且 host ∉ {localhost, 127.0.0.1, ::1, [::1]} 且未启用 TLS | 返回 error（启动失败，错误信息说明如何启用） |
| 其他 | 返回 nil（明文，行为不变） |

接线：

- `RedisConfig` 加 `EnableTLS bool`，由 `NewRedisTemplateStore` 内部调 helper 得到 `*tls.Config` → `redis.Options.TLSConfig`（config 结构体不携带 `*tls.Config`，保持可序列化、可表驱动测试）。
- `PoolStateStoreConfig` 加 `EnableTLS bool` → `buildPoolStateStore` 调同一 helper。
- 强制校验放在构造期（`NewRedisTemplateStore` / `buildPoolStateStore`），失败即 adapter 构造失败，fail-fast。

### 3.6 A4 — idle 状态按镜像指纹隔离（opensandbox）

`buildPoolStateStore` 签名改为 `buildPoolStateStore(imageURI string)`：

```go
// 生效 KeyPrefix：
//   <configured prefix 或 "opensandbox:pool"> + ":img-" + hex(sha256(imageURI))[:16]
```

- `defaultNewPool(ctx, poolID, imageURI, size)` 已持有 imageURI，调用点顺改。
- 语义：pool ID + 镜像不变 → namespace 不变，跨重启复用保留；镜像变更 → 新 namespace，旧 namespace 的 idle 条目对任何新池不可见，随 IdleTimeout 服务端过期。
- memory store（返回 nil）路径不受影响。

## 4. API Surface

`DELETE /api/v1/warm-pools/{warmPoolID}` 新增可选 query param：

| Param | 类型 | 默认 | 说明 |
|---|---|---|---|
| `force` | boolean (`strconv.ParseBool` 语法：`true/1/t/T/...`) | `false` | 强制删除语义，见 §3.2 |

- `force` 值非法 → `400 Invalid force parameter`。
- handler（`DeleteWarmPoolHandler`）解析后构造 `*adapter.WarmPoolDeleteOptions` 传给各 adapter；遍历 registry 的行为不变。
- 响应不变：成功 `204`，全部 adapter 失败 `404`。

**OpenAPI**（`docs/api/openapi.yaml`）：`/warm-pools/{warmPoolID}` 的 `delete` 增加 `force` parameter 描述。

## 5. Configuration

`configs/e2bgateway-example.yaml` 更新（opensandbox backend 下）：

```yaml
# templateStore 与 warmPool.stateStore 的 redis 配置均支持：
templateStore:
  type: redis
  addr: "redis.example.com:6379"   # 或 "rediss://..." 隐式启用 TLS
  password: "..."
  tls: true                         # password 非空且 addr 非 loopback 时必须为 true（或用 rediss://）
warmPool:
  stateStore:
    type: redis
    addr: "redis.example.com:6379"
    password: "..."
    tls: true
```

Viper 小写化注意：`tls`、`addr`、`password` 均为单段小写 key，无 camelCase 变体问题；解析仍走既有 `lookupString`/`lookupBool` helper。

无新增环境变量。

## 6. Security Considerations

- **A3 是安全修复本体**：消除 CWE-319（密码明文传输）。强制校验采用 fail-fast（构造期报错），不允许静默降级为明文。loopback 豁免便于本地开发/miniredis 测试。
- TLS 校验用系统 CA + ServerName 匹配，**不提供** `insecureSkipVerify`（避免提供绕过通道；确有自签需求时另开 issue 设计 CA 配置）。
- A4 消除跨镜像的 stale sandbox 复用（数据完整性 + 防止报告错误 template 身份）。
- #80 force 删除 agentsandbox 时仅删 unclaimed（ownerRef 校验），claimed 沙箱（用户正在使用）绝不受影响；`force` 来自已认证的管理 API（沿用 `warmPoolWrite` 中间件链），无新增注入面（query param 经 `ParseBool` 严格解析）。
- `NameHash(poolID)` 作为 label 值查询，poolID 已经过 `ValidateWarmPoolID` 校验（CRUD 创建路径），无注入风险。

## 7. Observability

沿用现有 `slog` 结构化日志约定：

- A1：刷新失败 warn 日志已存在，增加 `nextRetryIn` 字段（`warmPoolRoutingRetryBackoff`）。
- agentsandbox force：每个被直接删除的 Sandbox 记 `slog.Info("force-deleting unclaimed pool sandbox", "adapter", ..., "warmPoolID", ..., "sandbox", ...)`；删除失败记 warn（含 `error`）。
- opensandbox force：`slog.Info("warm pool force delete", "adapter", ..., "warmPoolID", ...)` 一条即可（drain 数量已由现有 released 计数覆盖）。
- A3 强制校验失败的 error 信息包含 addr 与修复指引（`enable tls: true or use rediss://`），不含密码。

## 8. Testing Strategy

单测（均跑 `-race`，CI 门槛 `make pre-commit`）：

| 项 | 测试 |
|---|---|
| A1 | fake ExtensionsClient 令 List 失败 → 第一次 `resolveWarmPool` 触发刷新；backoff 窗口内再次调用不再 List（action 计数）；窗口过后可重试 |
| A2 | Delete 成功 + 后续 List 失败 → `warmPoolMap` 不含被删池条目 |
| A3 | `redisTLSConfig` 表驱动：密码+远程无 TLS → error；`rediss://` → TLS 启用且 scheme 剥离；loopback+密码无 TLS → nil；factory 解析 `tls: true` 接线正确 |
| A4 | `buildPoolStateStore(imageURI)` 生成的 KeyPrefix 含镜像指纹；不同镜像 → 不同前缀；同镜像 → 稳定前缀 |
| #80 opensandbox | fake `warmPool` 记录 `Shutdown(graceful)` 实参：force → `graceful=false` + 仍调 `ReleaseAllIdleParallel`；graceful → `graceful=true`（现行为回归） |
| #80 agentsandbox | fake client：池 + 3 个带 label 的 Sandbox（1 个 claimed=ownerRef SandboxClaim、1 个 pool-owned、1 个孤儿）→ force 只删后两者；非 force 不直接删任何 Sandbox；CRD Delete 带 Background propagation |
| #80 handler | `?force=true` → opts.Force=true 传到 adapter（test/mock 断言）；`?force=garbage` → 400；无 param → graceful |
| 接口 | e2bcloud / mock 编译级签名更新；`test/mock` 调用记录含 opts |

无新增 E2E（现有 warm pool E2E 结构不变；force 语义依赖真实 controller/Redis 的部分由上述 fake 层覆盖）。

## 9. Migration / Rollout

- **Breaking change（Go 层）**：`SandboxAdapter.DeleteWarmPool` 签名变更。仓库内 4 个 adapter + test/mock 一次改齐；外部自定义 adapter（若有）需跟改——在 PR 描述与 CHANGELOG 注明。
- **HTTP 层向后兼容**：不带 `force` param 的 DELETE 行为与现状完全一致（agentsandbox 唯一差异是 DeleteOptions 加了 Background propagation——当前无 ownerRef 依赖 CRD 的级联行为变化，Sandbox 由 controller 逻辑管理，见 §10 Q1）。
- **A3 强制校验可能 break 现有部署**：已配置 `password` + 远程 Redis + 无 TLS 的部署升级后 adapter 构造失败。这是有意的 fail-fast 安全行为；release note 中给出迁移指引（加 `tls: true` 或 `rediss://`）。
- A4 上线后第一次启动：现有 Redis 中旧 prefix 的 idle 条目全部不可见（等价一次性冷启动），旧条目随 TTL 过期，无需手动清理。

## 10. Open Questions

| # | 问题 | Owner | 期限 |
|---|---|---|---|
| Q1 | agentsandbox CRD 删除加 `PropagationPolicy: Background` 对**非 force** 路径是否完全无行为变化（Sandbox 的 ownerRef 是 WarmPool CRD，Background GC 是否会连带删掉 claimed 沙箱？）——实现前需在 `sandboxwarmpool_controller.go` / claim 领养逻辑中确认 claimed 沙箱 ownerRef 已改指 SandboxClaim，若是则 GC 不会误删 | 实现者（plan Phase 1 第一项） | 编码前 |
| Q2 | `warmPoolRoutingRetryBackoff` 取 30s（= TTL）是否合适，还是需要独立可配置 | 本 spec 默认 30s 常量，PR review 时可再议 | PR review |
