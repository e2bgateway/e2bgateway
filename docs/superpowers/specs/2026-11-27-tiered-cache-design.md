# 两级缓存组合：L1 内存 + L2 Redis 与跨副本失效

**Issue**: #113 — [v0.2.0][feat] 两级缓存组合：L1 内存 + L2 Redis 与跨副本失效（设计 §15.2）
**Date**: 2026-11-27
**Priority**: P2
**Approach**: A — 泛型 `TieredCache[T]` 组合器 + handler 层显式接入（不改 `SandboxAdapter` 接口）

---

## 1. Problem Statement

设计文档 §15.2 定义了 L1（in-memory LRU，per-pod）+ L2（Redis，共享）两级缓存。现状：

- `internal/cache/cache.go`（内存 LRU）与 `internal/cache/redis.go`（RedisCache）各自独立，没有组合成两级结构；`RedisCache` 无生产调用方，`config.CacheConfig` 从未被 server 启动流程读取。
- 热点读路径全部直打后端：`GetSandboxHandler`（`internal/api/v1/handlers.go:169`）、`ListTemplatesHandler`/`ListTemplatesHandlerV2`（`handlers.go:633/653`）每次请求都调 adapter；`/readyz`（`ReadyHandler`，`handlers.go:1210`）每次探测 `HealthCheckAll` 扇出到所有后端。
- 多副本部署下，即使将来加单层缓存，副本间也会在一个 TTL 窗口内读到脏数据。

## 2. Design Goals

- `TieredCache[T]`：读 L1 → miss 读 L2 → miss 回源并回填两级；写 write-through（先 L2 后 L1）。
- 跨副本失效：Redis Pub/Sub 广播 invalidate，各副本收到后清 L1。**验收：2 副本下模板/sandbox 变更 1s 内两副本可见。**
- 热点路径接入（按设计文档 TTL）：sandbox metadata 30s、template list 60s、backend health（/readyz 聚合结果）10s 仅 L1。
- 降级：L2 不可用时退化为纯 L1 + WARN + metric，不阻塞、不失败请求。
- 命中率 metrics：`l1_hits` / `l2_hits` / `misses` / `invalidations`（+ `l2_errors`）。
- 默认关闭（`cache.tiered.enabled: false`），升级零行为变化。

**Non-goals（本期不做）**：

- Tenant config 缓存：代码中不存在 tenant config 存储/读路径（租户身份由 auth 中间件从静态密钥/JWT 解析，限流 override 来自静态配置）。issue 中该条记为 N/A，待将来出现真实读路径再接入。
- L2 承载 rate-limit 计数器、quota（§15.2 中 L2 的其他职责）。
- Watch-based K8s 失效（§15.2 第三种策略）。
- `ListSandboxes` 列表缓存、access token 缓存。
- 不改 `SandboxAdapter` 接口，四个 adapter 均不动。

## 3. Architecture

### 3.1 新增类型（`internal/cache/`）

```go
// tiered.go — 泛型两级缓存组合器。T 为缓存值类型（JSON 可序列化）。
type TieredOptions struct {
    Name      string        // metrics 标签：sandbox_metadata / template_list
    L1MaxSize int           // 默认 10000
    L1TTL     time.Duration // L1 默认 TTL；GetOrLoad 可按 key 覆盖
    L2        *RedisCache   // nil => 纯 L1 模式（enabled 但未配 address 时）
    Timeout   time.Duration // 单次 L2 操作超时，默认 200ms
}

func NewTiered[T any](opts TieredOptions) *TieredCache[T]

// 读路径：L1 → L2（命中回填 L1）→ load 回源（回填先 L2 后 L1）。
// singleflight 合并同 key 并发回源，防击穿。
func (tc *TieredCache[T]) GetOrLoad(ctx context.Context, key string, ttl time.Duration,
    load func(ctx context.Context) (T, error)) (T, error)

// 写路径 write-through：先 L2 后 L1，成功后广播 invalidate（其他副本清 L1，
// 下次读从 L2 拿到新值）。本副本 L1 已是新值，不自我清除。
func (tc *TieredCache[T]) Set(ctx context.Context, key string, val T, ttl time.Duration)

// 失效：删 L2 → 删本地 L1 → 广播 invalidate。
func (tc *TieredCache[T]) Invalidate(ctx context.Context, key string)

// 订阅回调：其他副本广播到达时删本地 L1，记 invalidations metric。
func (tc *TieredCache[T]) onRemoteInvalidate(key string)
```

L1 复用现有 `*Cache`（值以 `any` 存 `T`）；L2 复用现有 `*RedisCache`（string 值，`encoding/json` 序列化 `T`）。L2 反序列化失败按 miss 处理（记 WARN），避免坏数据卡死读路径。

```go
// invalidator.go — Pub/Sub 失效广播。直接使用 redis.UniversalClient
//（与 sessionstore.NewRedisWithClient 相同的 miniredis 测试缝）。
type Invalidator struct { /* client, channel, instanceID, handlers */ }

// instanceID = hostname + 随机后缀，用于跳过自己发出的消息。
func NewInvalidator(client redis.UniversalClient, channel string) *Invalidator

// Register 挂载某缓存的 onRemoteInvalidate（按消息中的 cache name 分发）。
func (inv *Invalidator) Register(name string, fn func(key string))

// Broadcast 发布 {cache, key, sender} JSON 消息。失败仅 WARN + metric。
func (inv *Invalidator) Broadcast(ctx context.Context, cacheName, key string)

// Run 阻塞订阅循环：断线指数退避重连（1s 起，上限 30s），ctx 取消退出。
// 断线期间失效退化为各副本 L1 TTL 自然过期（有界陈旧）。
func (inv *Invalidator) Run(ctx context.Context)
```

一个进程共用一个 `Invalidator`（一个订阅连接、一个 channel），多个 `TieredCache` 实例按 name 注册回调。

### 3.2 数据流

```
读（GetOrLoad）                          写（Set / Invalidate）
─────────────                          ─────────────────────
L1.Get(key) ──hit──► 返回               Set: L2.SetWithTTL ──► L1.SetWithTTL
   │miss                                      │（L2 失败：跳过，仍写 L1）
L2.Get(key) ──hit──► JSON 解码                ▼
   │  回填 L1，返回                     Invalidator.Broadcast(name,key)
   │miss/err（降级）                          │ Pub/Sub
   ▼                                          ▼ 其他副本
singleflight load(ctx) ──► 回源         onRemoteInvalidate: L1.Delete(key)
   │ 回填：先 L2 后 L1                   （sender == 自己 ⇒ 忽略）
   ▼
返回
```

### 3.3 降级语义

- 每次 L2 操作（Get/Set/Del/Broadcast）套 `Timeout`（默认 200ms）的 context，Redis 挂掉不会拖慢请求。
- L2 出错：`l2_errors_total` +1，WARN 日志（按 name 节流，最多每 10s 一条），读路径视为 miss 继续回源，写路径跳过 L2 仍写 L1。**任何 L2 错误都不使请求失败。**
- Pub/Sub 断线：Run 循环退避重连 + WARN；期间跨副本一致性退化为 L1 TTL 上界（30s/60s）。

### 3.4 热点路径接入（handler 层）

handler 构造函数追加 cache 参数，`nil` = 直通（现状行为）。`server.New`（`internal/server/http.go:83`）按配置构造并注入；`internal/cmd/serve.go` 无需改动。

| 路径 | 缓存实例 / key | TTL | 失效触发 |
|---|---|---|---|
| `GetSandboxHandler` | `TieredCache[dto.SandboxInfo]`，key `sbx:{backend}:{sandboxID}` | 30s | Kill/Pause → `Invalidate`；Create/Resume → `Set`（write-through，值用 `sandboxToDTO`，不含 token） |
| `ListTemplatesHandler` / V2 | 共享 `TieredCache[[]*adapter.Template]`，key `tmpl:{backend}`（per-adapter 缓存原始列表，合并/DTO 转换每请求做，开销可忽略） | 60s | Create/Delete/UpdateTemplate → `Invalidate` 对应 backend 的 key |
| `ReadyHandler`（/readyz） | 现有 `*cache.Cache` 仅 L1，key `readyz`，值为聚合结果 struct | 10s | 无（TTL 自然过期；router healthCheckLoop 不受影响） |

- sandbox key 含 backend 名，避免跨后端同 ID 冲突；adapter 路由解析（`resolveAdapterForSandbox`）本身不缓存。
- 失效接线点：`KillSandboxHandler`/`PauseSandboxHandler`/`ResumeSandboxHandler`/`CreateSandboxHandler`、`CreateTemplateHandler`(V2)/`DeleteTemplateHandler`/`UpdateTemplateHandler` 成功分支。Pause/Delete 这类循环尝试 adapter 的 handler，只对成功的 adapter 做失效。
- 缓存关闭时上述 handler 全部走原路径，零开销。

## 4. API Surface

无 HTTP API 变化：不改路由、请求/响应 schema、错误码。缓存命中与回源返回完全相同的 `dto.SandboxInfo` / `[]*dto.TemplateInfo` / `[]dto.V2TemplateInfo`。`docs/api/openapi.yaml` 无需更新。

## 5. Configuration

新增 `cache.tiered` 子树（`internal/config/config.go`，现有 `cache.backend/memory/redis` 保留不动）：

```yaml
cache:
  tiered:
    enabled: false            # 默认关闭；开启后未配 l2.address 则为纯 L1 模式
    l1:
      maxSize: 10000
      defaultTTL: 30s         # 各路径按 §3.4 覆盖（30s/60s/10s）
    l2:
      address: ""             # host:port；空 => 不建 Redis 客户端
      password: ""
      db: 0
      tls: false
      keyPrefix: "e2bgw:cache:"
      pubsubChannel: "e2bgw:cache:invalidate"
      timeout: 200ms          # 单次 L2 操作超时
```

- 结构体：`TieredCacheConfig{Enabled bool; L1 TieredL1Config; L2 TieredL2Config}`，`TieredL2Config` 内嵌复用 `RedisConfig{Address,Password,DB}` + `TLS/KeyPrefix/PubsubChannel/Timeout`。
- Viper 会把 map key 小写化：`mapstructure` 标签匹配大小写不敏感，`defaultTTL`/`defaultttl`、`maxSize`/`maxsize`、`pubsubChannel`/`pubsubchannel` 均可解析；测试覆盖两种写法。
- 默认值注册进 `setDefaults`（`config.go:295` 附近）；env 前缀 `E2BGW_`（如 `E2BGW_CACHE_TIERED_ENABLED`）。
- 校验（`Validate`）：`enabled && l2.address != ""` 时校验 address 格式（`redisx.NormalizeRedisAddr`）；`timeout > 0`；`l1.maxSize > 0`。
- 各路径 TTL（30s/60s/10s）为代码常量，不做配置项（YAGNI；issue 按设计文档 TTL 固定）。

## 6. Security Considerations

- **缓存值不含凭证**：已核实 `dto.SandboxInfo`（`internal/api/dto/e2b.go:55`）与 `TemplateInfo` 均不含 `envdAccessToken`/`trafficAccessToken`（token 只出现在 create/resume 响应，write-through 缓存的是 `sandboxToDTO` 产物，token 字段天然不在其中）。因此 L2 不存储 bearer 凭证。
- TLS 策略（沿用 `internal/redisx` helpers）：非回环 address 且未开 TLS → 启动时显著 WARN（缓存值含用户 metadata，建议 TLS；因不含凭证，不像 sessionstore 那样 fail-closed）。`password != ""` 且非回环明文 → 拒绝启动（密码本身不应明文传输）。
- Pub/Sub 消息只含 `{cache name, key, sender}`，不含缓存值；key 中的 sandboxID/templateID 来自 URL 参数，仅用作 Redis key 与 map key，不拼接命令（go-redis 参数化，无注入面）。
- 失效广播可被同 Redis 内其他写入方伪造 → 后果仅为额外回源（cache miss），无数据泄露/篡改面；Redis 访问控制由部署侧负责（与 sessionstore 同一信任边界）。
- 缓存命中仍走完整 auth/authz 中间件链（缓存在 handler 内，不在中间件前），无越权读风险。

## 7. Observability

新增 Prometheus counters（注册进 `metrics.Init`，`internal/metrics/metrics.go:78`，私有 registry 模式），统一标签 `cache`（`sandbox_metadata` / `template_list`）：

| Metric | 含义 |
|---|---|
| `e2bgw_cache_l1_hits_total{cache}` | L1 命中 |
| `e2bgw_cache_l2_hits_total{cache}` | L2 命中（含回填 L1） |
| `e2bgw_cache_misses_total{cache}` | 两级皆 miss、触发回源 |
| `e2bgw_cache_invalidations_total{cache}` | 收到远端广播并清除本地 L1 的次数 |
| `e2bgw_cache_l2_errors_total{cache}` | L2 操作失败（降级）次数 |

包级 recorder 沿用现有模式（`metrics.RecordLifecycle` 等）：`metrics.RecordCache(name, event string)`，metrics 未 Init 时 no-op。`internal/cache` 导入 `internal/metrics`（无环：metrics 不依赖 cache）。

日志（slog 结构化）：L2 降级 WARN（字段 `cache`、`op`、`error`，节流）；Pub/Sub 重连 WARN/INFO；启动时 INFO 记录 tiered 模式（L1+L2 / L1-only / disabled）。

## 8. Testing Strategy

全部 testify + 表驱动 + `-race`（AGENTS.md 测试规范）。

**单元（`internal/cache/tiered_test.go`、`invalidator_test.go`，miniredis）**：
- 命中路径：L1 hit 不触 L2；L2 hit 回填 L1；双 miss 回源并回填两级（先 L2 后 L1 顺序可观测）。
- TTL：L1/L2 各自过期后回源；per-key TTL 覆盖。
- write-through：`Set` 后 L2、L1 均为新值；`Invalidate` 后两级皆空。
- 降级：关闭 miniredis 后 `GetOrLoad` 仍成功（纯 L1 + 回源），`l2_errors` 递增，无请求失败；L2 慢（超时）不阻塞超过 Timeout。
- singleflight：N 并发同 key 只回源一次。
- 失效广播：两个 `TieredCache` + 各自 `Invalidator` 共享一个 miniredis：A `Invalidate`/`Set` → B 的 L1 在 1s 内清除；A 自己的 L1 不被误清（sender 跳过）。
- 坏 JSON：L2 存非法值 → 按 miss 回源。
- 配置：camelCase / lowercase 两种 key 写法解析一致；Validate 拒绝非法组合。

**集成（`test/integration/tiered_cache_test.go`）**：
- 2 副本一致性（进程内两套完整缓存栈 + 共享 miniredis，走真实 handler）：副本 A 改模板/sandbox → 副本 B 1s 内可见（issue 验收标准）。

**E2E（`test/e2e/multi_replica_cache_test.go`，沿用 `multi_replica_test.go` 的 `GATEWAY_URL`/`GATEWAY_URL_B` 门控模式，未设 env 则 skip）**：
- 双副本 + 共享 Redis：A 创建 sandbox → B GET（预热缓存）→ A Kill → B 1s 内 GET 返回 404。
- metrics 端点断言 `l1_hits`/`l2_hits`/`misses`/`invalidations` 计数出现且方向正确（热点读后端 QPS 下降的佐证）。

## 9. Migration / Rollout

- 默认 `enabled: false`，合并后行为与现状逐字节一致；无数据迁移。
- 开启顺序建议：先单副本 `enabled: true`（不配 l2.address，纯 L1）观察命中率与延迟 → 配 L2 单副本 → 多副本。
- 回滚 = 关配置开关；Redis 中残留 key 按 TTL 自然过期。
- `configs/e2bgateway-example.yaml` 增加注释掉的 `cache.tiered` 示例；`AGENTS.md` key-packages 表更新 `internal/cache/` 描述。

## 10. Open Questions

无阻塞项。已决策记录：
- 接入范围：sandbox metadata + template list + /readyz 健康聚合；tenant config 记 N/A（2026-11-27 与 issue 提出者确认）。
- 默认关闭（同上确认）。
- 方案 A（handler 层接入）否决 adapter 装饰器方案 B：避免转发 ~30 个接口方法、避免健康检查缓存干扰 router healthCheckLoop。