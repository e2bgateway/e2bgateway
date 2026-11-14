# OpenSandbox SDK 能力采纳 Phase 2：PoolManager.Destroy + WithRetry + 缓存瘦身 + go/js 示例

**Issue**: #85 — [技术优化] 采用 OpenSandbox Go SDK v1.2.1（Phase 2）
**Date**: 2026-11-13
**Priority**: P2
**Approach**: A（保守采纳）——force 路径采纳 `SandboxPoolManager.Destroy`；lifecycle client 加 `WithRetry`；`endpointHeaders` 缓存实证瘦身；补 go/js warm pool 示例并纳入 CI

**前置**: Phase 1（SDK v1.2.1 + 真实 server E2E）已随 #87 合入 main；本 branch `opensandbox-sdk-phase2` 基于该 main。

---

## 1. Problem Statement

#85 盘点了 SDK v1.2.x 已提供、gateway 手写或未用的能力。Phase 1 完成了版本升级与 E2E 基建，本 Phase 落地剩余的采纳项：

- `DeleteWarmPool` 的 force 路径手写 `ReleaseAllIdleParallel` drain，缺少跨副本协调：共享 Redis state store 的多 gateway 副本部署下，force 删除无法阻止其它副本的 pool reconciler 继续补充，也没有 tombstone 防止旧 namespace 数据被重绑定（#82 A4 的残留面）。
- lifecycle client 未启用 SDK 的重试（`WithRetry`），瞬时网络故障直接透传给调用方。
- gateway 自建 `endpointHeaders` 缓存（`internal/cache`，3 处 Set / 1 处 Get）与 SDK `LifecycleClient` **内建**的 `EndpointCache`（`NewLifecycleClient` 默认 1024 条/600s TTL，`GetOrFetch`）部分重复。
- examples 缺 go / javascript 的 warm pool 示例（python 版已在 Phase 1 修复并进 CI）。

## 2. Design Goals

- force delete 采纳 `SandboxPoolManager.Destroy(Strategy: PoolDestroyForce)`：获得 DESTROYING fence（peer 停止补充/让出 primary lock）、store drain+kill、DESTROYED tombstone、`DrainedIdleCount/KilledIdleCount` 可观测计数。
- graceful delete 路径**零行为变化**（`Destroy` 仅支持 FORCE 策略，与 graceful"等待 in-flight"语义不匹配）。
- lifecycle client 启用 SDK 默认 `WithRetry`（内建非幂等请求保护）。
- `endpointHeaders` 缓存**整体删除**（trace 已完成：唯一消费者的新鲜数据总是覆盖缓存，且存在 key 永不匹配的死写入，见 §3.4）。
- `examples/go/warm_pools` 与 `examples/javascript/warm_pools.js` 落地并纳入 E2E runner 的 HTTP-only 段（真实 server 活体验证）。

**Non-goals：**

- graceful 路径改用 Destroy（语义冲突）。
- `RetryConfig` 定制调参（用 SDK 默认；出现真实需求再配置化）。
- tokenCache（gateway 自生成 `envd_{id}_{random}` token，与 SDK 无关）不动。
- agentsandbox / e2bcloud adapter 不动。

## 3. Architecture

### 3.1 PoolManager 接入（opensandbox）

**Store 显式化**：`buildPoolStateStore(imageURI)` 改为总是返回非 nil store——memory 配置返回 `opensandbox.NewInMemoryPoolStateStore()`（与 SDK pool 内部默认等价；实现时验证 `Build()` 对显式 memory store 的行为一致），redis 配置不变（`poolredis` + TLS + 镜像指纹 prefix，Phase 1 产物）。

**poolEntry 扩展**：

```go
// poolDestroyer is the adapter-facing subset of
// *opensandbox.SandboxPoolManager (tests substitute fakes).
type poolDestroyer interface {
    Destroy(ctx context.Context, poolName string,
        options opensandbox.PoolDestroyOptions) (*opensandbox.PoolDestroyResult, error)
}

type poolEntry struct {
    pool       warmPool
    destroyer  poolDestroyer   // nil only in legacy fakes; force delete requires it
    ...
}
```

`defaultNewPool` 构造 manager：`NewSandboxPoolManagerBuilder().StateStore(store).ConnectionConfig(cc).Build()`（OwnerID 用 builder 默认生成），与 pool 共享同一 store 实例；`newPoolFn` 测试替身路径 destroyer 可为 fake。

**DeleteWarmPool force 分支**：

```
force:  Shutdown(ctx, false) → destroyer.Destroy(ctx, id, {Strategy: PoolDestroyForce,
            TombstoneTTL: &forceDeleteTombstoneTTL})
        → slog.Info 输出 DrainedIdleCount/KilledIdleCount
        → PoolDestroyIncompleteError / 其它错误：包装上抛（SDK 文档保证重试 Destroy 安全）
graceful: Shutdown(ctx, true) → ReleaseAllIdleParallel（现状不变）
```

**Tombstone TTL 决策（深入分析结论）**：SDK 默认 `DefaultPoolTombstoneTTL = 7 天`，且 `DefaultSandboxPool.Start()` 会拒绝绑定被 tombstone 的 namespace（`ensureNamespaceActive` → `PoolDestroyedError`）——用默认值会让**显式同 ID 重建被挡一周**，不可接受。store 接口无清除 API，但 `MarkDestroyed` 的 TTL 由 `DestroyOptions.TombstoneTTL` 传入、过期即读回 ACTIVE。故取 `forceDeleteTombstoneTTL = 5 * time.Minute`（新常量）：足够覆盖并发 teardown 窗口内 fence peer / 防静默 rebind，又允许管理员几分钟内有意的同 ID 重建。

**CreateWarmPool 配套**：`pool.Start` 返回 `PoolDestroyedError` 时（`errors.As` 判定），包装为可操作错误："pool namespace %q was force-deleted recently; retry after the tombstone expires (~5m) or choose a different warmPoolID"，并走既有 rollback 路径。

`poolStarting` ID 预留、opMu 串行、错误包装格式全部保持。memory store 下 Destroy 同样可用（fence/tombstone 落在进程内 store，语义退化为单副本，无害）。

**graceful 不动的深入论证**：① `Destroy` 仅支持 FORCE 策略（SDK 显式拒绝其它值）；② graceful 契约（#80/#84）是"等待 in-flight 完成"，由 `Shutdown(true)` 的 draining 状态机承担，Destroy 无等待语义；③ 若 graceful 后追加 Destroy，会写 tombstone 阻断同 ID 重建——对优雅删除是非预期的语义变更；④ graceful 的跨副本 replenish 竞态是既有行为且属于多副本 registry 一致的更大议题，不在本期扩scope。

### 3.2 WithRetry

`New(cfg)`：`opensandbox.NewLifecycleClient(cfg.BaseURL, cfg.APIKey, opensandbox.WithRetry(<SDK 默认 RetryConfig>))`（默认配置构造函数名以 retry.go 实际导出为准，如 `DefaultRetryConfig()`）。SDK 的 `requestSentError` 机制保证已发出的非幂等请求不盲目重试。ExecdClient 不加（数据面调用语义由上层超时控制，避免命令重复执行风险）。

### 3.3 ExecdClient 构造不变

`getOrCreateExecdClient` 的 execdClients 缓存（client 对象复用）保留——它缓存的是构造好的 client，不是 endpoint 数据。

### 3.4 endpointHeaders 缓存瘦身（trace 已完成，决策：**整体删除**）

现状 trace 结论（2026-11-13，adapter.go）：
- **Get 仅 1 处**：`getOrCreateExecdClient`（L281）merge 进 execd client headers，且 merge 语义为"缓存先拷贝、当次 `ep.Headers` 覆盖"——缓存只可能补充新鲜响应中**不存在**的 key。
- **Set 3 处**：`GetPortURL`（L1025，key 为 `{sandboxID}-{port}`——与 Get 的 `{sandboxID}` key **永不匹配，是死写入**）、`GetAccessToken`（L1085）、`GetEnvdEndpoint`（L1268）。
- 三处 Set 与该 Get 的数据源都是同一 server 的 `GetEndpoint`/`GetSignedEndpoint` 响应，header schema 相同（auth cookie/签名/路由提示）；execd client 构造前**本就调用**其中之一拿到新鲜 `ep.Headers`。

**决策**：删除 `endpointHeaders` 字段、初始化、全部 3 处 Set 与 Get 处 merge；`getOrCreateExecdClient` 直接使用当次 endpoint 响应的 `ep.Headers`。理由：唯一消费者的新鲜数据总是覆盖缓存；死写入无消费者；unsigned 路径的复用已由 SDK 内建 `EndpointCache`（600s TTL）承担，手工缓存属重复实现。

**行为不变式**（测试保障）：signed/unsigned 两路径下 execd client 收到的 headers 与删除前"merge 后"结果一致（新鲜 headers 本就优先）；envd proxy / GetAccessToken / GetPortURL 的对外行为不变。真实 server E2E（commands/files/ports/token 全数据面用例）作为最终验证。

### 3.5 examples

- `examples/go/warm_pools/main.go`：纯 `net/http`（沿用 hello_world 风格与 license header），流程与 python 版对齐：list → create(targetSize 3) → get → resize(5) → graceful delete → create(1) + `?force=true` delete；任一步 HTTP ≥400 即 `os.Exit(1)` 并打印响应体；创建的池在失败路径用 `defer` best-effort force 清理（与 python 版 finally 对齐）。
- `examples/javascript/warm_pools.js`：**原生 fetch**（Node ≥18，无 npm 依赖——warm pool 不在 E2B SDK 面内），同样六步 + 失败非零退出 + finally 清理；CommonJS（与现有 js 示例一致）。
- runner（`hack/kind-e2e/run-examples-e2e.sh`）：
  - Go loop 列表加 `warm_pools`（输出断言沿用现有 grep 模式，warm_pools 打印 "Done."——纳入期望输出集合）。
  - "Python HTTP-only Examples" 段扩为 **HTTP-only Examples**：python `warm_pools.py` 之后跑 `node ./examples/javascript/warm_pools.js`（无 npm install 依赖，不受 `SKIP_SDK_TESTS` 门控；node 20 已在两个 job setup）。
- `examples/README.md` 若含示例清单则同步加两行。

## 4. API Surface

无 gateway HTTP API 变更。examples 只消费既有端点（与 `docs/api/openapi.yaml` 一致，含 `?force=true` 与 400 语义）。

## 5. Configuration

无新增配置。`WithRetry` 用 SDK 默认参数，不暴露配置项（Non-goal）。

## 6. Security Considerations

- Destroy 的 fence/tombstone 提升多副本下 force 删除的隔离性：被 fence 的 peer 不能再向该 namespace 发布 idle，降低"删除中池被其它副本补充出孤儿沙箱"的资源泄漏面。
- WithRetry 只作用于 lifecycle client（幂等 GET 与 SDK 判定可安全重试的请求）；ExecdClient 明确不加重试，避免命令/文件操作重复执行的副作用风险。
- endpointHeaders 瘦身不得降低 signed endpoint 的 header 传递完整性（OSEP-0011 路由/鉴权 header 必须随当次响应透传）——§3.4 规则 5 为不变式，单测覆盖 signed 路径。
- examples 无凭证处理变化。

## 7. Observability

- force delete 成功路径日志增加 `drainedIdle` / `killedIdle` 计数（来自 `PoolDestroyResult`）。
- Destroy 返回 `PoolDestroyIncompleteError` 时 warn 日志带 `reason`（namespace 停留 DESTROYING，重试安全）。
- 其余日志字段沿用现有约定（adapter / warmPoolID）。

## 8. Testing Strategy

| 层 | 内容 |
|---|---|
| 单测（-race） | fake `poolDestroyer`：force → `Destroy` 被调且 Strategy=Force、计数入日志路径；graceful → destroyer 不被调、ReleaseAllIdleParallel 照旧；Destroy 错误 → 上抛且池已从 registry 摘除的语义与现状一致；memory store 显式化后 CreateWarmPool/DeleteWarmPool 既有测试全过 |
| 单测 | endpointHeaders：按 §3.4 删除后，execd client 构造使用当次 ep.Headers（signed/unsigned 两路径）；envd proxy 相关既有测试不回归 |
| E2E（真实 server） | 既有 warm pool live 段（create → warmup → claim 证据 → force delete → claimed 存活）直接覆盖 Destroy 路径；HTTP-only 段新增 go/js warm_pools 活体执行 |
| 门槛 | `make pre-commit`；两个 E2E job 绿 |

## 9. Migration / Rollout

- 无配置/接口 breaking change。行为差异仅 force delete 内部机制（drain 由 store 级 Destroy 完成，附带 fence/tombstone）——外部可见语义（204、claimed 不受影响、池消失）不变。
- memory store 部署：Destroy 的 fence/tombstone 限于进程内，无跨副本效果（本来也无共享 store），行为等价升级。
- Redis store 部署：升级后首次 force delete 开始写 tombstone；旧数据无 tombstone，不受影响（tombstone 只约束新 Destroy 后的重绑定）。
- examples 为新增文件，无迁移。

## 10. Open Questions

| # | 问题 | Owner | 期限 |
|---|---|---|---|
| Q1 | `Build()` 对显式传入 `NewInMemoryPoolStateStore()` 与 nil（内部默认）的行为是否完全一致（pool 重建时 idle 复用路径） | 实现者（SDK 源码核对 + 单测） | Task 1 编码前 |
| Q2 | `WithRetry` 默认配置构造函数名与字段（retry.go:132 附近导出的实际 API） | 实现者 | Task 2 编码时 |
| Q3 | ~~endpointHeaders 隐藏消费者~~ **已解决（2026-11-13 trace）**：Get 仅 1 处且新鲜数据覆盖缓存，L1025 Set 为 key 不匹配的死写入 → 整体删除，见 §3.4 | 已核实 | 完成 |
