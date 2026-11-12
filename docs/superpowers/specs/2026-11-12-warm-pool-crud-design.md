# Warm Pool CRUD — Agent-Sandbox + OpenSandbox 功能打平

**Issue**: #32 — [M1-R1.2][功能打平] Agent-Sandbox + OpenSandbox: Warm Pool CRUD
**Date**: 2026-11-12
**Priority**: P1
**Approach**: A — 复用现有 `SandboxAdapter` 5 方法接口；agentsandbox 映射到 `SandboxWarmPool` CRD，opensandbox 用 SDK 客户端池注册表 + 自动 Acquire 接入

---

## 1. Problem Statement

`SandboxAdapter` 接口已定义 Warm Pool CRUD 5 个方法（`internal/adapter/interface.go:78-82`），HTTP 路由、handler、DTO 均已就绪（`internal/server/http.go:269-273`、`internal/api/v1/handlers.go:761+`、`internal/api/dto/e2b.go:272-294`），且 e2bcloud / mock 已实现。

但 agentsandbox 与 opensandbox 两个 adapter 目前全是 stub：

- 4 个方法返回 `not supported` 错误，`ListWarmPools` 返回空（`agentsandbox/adapter.go:1173+`、`opensandbox/adapter.go:803+`）
- 结果是这两个后端上 `GET/POST/DELETE /api/v1/warm-pools` 全部不可用，与 e2b-cloud 行为不平齐

两个后端 SDK 均为最新版且都具备 warm pool 原语，但形态完全不同：

| | agent-sandbox v1.0.10 | OpenSandbox SDK v1.2.0 |
|---|---|---|
| 模型 | 服务端 CRD `SandboxWarmPool`（`extensions/api/v1beta1`），controller 调谐 | 进程内客户端池 `DefaultSandboxPool`，无服务端 pool API |
| CRUD 原语 | `ExtensionsClient.SandboxWarmPools(ns)` 类型化 clientset（Create/Get/List/Delete/Patch） | `NewSandboxPoolBuilder().Build()` + `Start` / `Snapshot` / `Resize` / `Shutdown` |
| 持久性 | 声明式，网关重启不丢 | 池注册表在进程内；`PoolStateStore` 可选 memory / Redis（`poolredis`） |

## 2. Design Goals

- agentsandbox：5 个方法映射到 `SandboxWarmPool` CRD，网关重启后 `List/Get` 结果不丢
- opensandbox：5 个方法映射到 adapter 内池注册表；**已拍板**：建池后 `CreateSandbox(templateID)` 自动优先 `pool.Acquire`，未命中/失败回落直连创建
- opensandbox StateStore：**已拍板**：memory 默认，配置 Redis 地址时用 `poolredis`
- 两个后端的 `WarmPool.Status` 字符串取值统一：`pending` / `scaling` / `ready` / `degraded` / `destroyed`
- 不改 `SandboxAdapter` 接口、不改 HTTP 层、不改 DTO

**Non-goals**：

- 不做跨网关实例的 opensandbox 池定义共享（Redis 只存 idle 沙箱记录与 primary lock，池定义仍在进程内）
- 不做 warm pool 的 HPA / 自动扩缩容
- 不改 e2bcloud / mock 的现有实现
- 不做 SandboxTemplate 的 CRUD（agentsandbox 建池假设 `TemplateRef` 指向的 SandboxTemplate 已存在，或 templateID 直接作为 ref 名）

## 3. Architecture

### 3.1 Interface changes

无。复用 `internal/adapter/interface.go` 现有 5 方法与 `WarmPool` / `WarmPoolCreateRequest` DTO。

### 3.2 Adapter behavior

#### agentsandbox（声明式，薄封装）

复用 adapter 已持有的 `a.k8s.ExtensionsClient`（`extensionsv1beta1.ExtensionsV1beta1Interface`，含 `SandboxWarmPools(ns)`，且 SDK 自带 fake clientset 可单测）。

字段映射：

| `adapter.WarmPool` | `SandboxWarmPool` CRD |
|---|---|
| `WarmPoolID` | `metadata.name` |
| `TemplateID` | `spec.sandboxTemplateRef.name`（读取时以 CRD 为权威来源；创建时见下方 TemplateRef 解析） |
| `TargetSize` | `spec.replicas` |
| `CurrentSize` | `status.readyReplicas` |
| `Status` | 推导，见下 |
| `CreatedAt` | `metadata.creationTimestamp` |

Status 推导规则：

```
status.observedGeneration < metadata.generation        → "pending"
status.replicas == 0 && spec.replicas > 0              → "pending"
status.readyReplicas >= spec.replicas                  → "ready"
status.readyReplicas >  0                              → "scaling"
其他                                                    → "pending"
```

TemplateRef 解析（Create 路径）——SDK 分析结论：高层 `sandbox.Client` 无 warm pool/template 管理方法（设计上池由运维预置），但 `ExtensionsClient`（`ExtensionsV1beta1Interface`）同时提供 `SandboxWarmPools(ns)` 与 `SandboxTemplates(ns)` 全套 typed CRUD，直接封装即可：

1. ref 名 = `templateToSandboxTemplate[templateID]`（可选配置覆盖），缺省 = templateID 本身
2. `ExtensionsClient.SandboxTemplates(ns).Get(refName)` 校验存在性；NotFound → 400，报错提示需先创建 SandboxTemplate
3. 建池成功后把 `templateID → warmPoolID` 写入运行时 `warmPoolMap`，新建的池无需重启即可被 `CreateSandbox` 消费

不复用 `TemplateToWarmPool` 反查：其语义是 templateID→池名（消费面映射），与 templateID→SandboxTemplate ref 名是两回事。

方法实现：

- `CreateWarmPool(req)`：`WarmPoolID` 为空时生成 `wp-{templateID}-{hash8}`（校验符合 K8s name 规范：小写字母数字与 `-`，≤63 字符）；按上述规则解析并校验 TemplateRef；构造 CRD 对象 `Create`；`AlreadyExists` → 返回 409 语义错误
- `GetWarmPool(id)`：`Get`；`IsNotFound` → `adapter.ErrWarmPoolNotFound`（新增哨兵错误，handler 映射 404）
- `ListWarmPools()`：`List` 后逐项转换
- `DeleteWarmPool(id)`：`Delete`（CRD controller 负责回收池中 unclaimed sandbox）
- `UpdateWarmPoolSize(id, n)`：`Patch`（merge patch `spec.replicas`）；n<0 拒绝

新建文件：`internal/adapter/agentsandbox/warmpool.go`（把 stub 从 `adapter.go` 挪出）。

#### opensandbox（进程内池注册表 + Acquire 接入）

adapter 新增字段：

```go
type Adapter struct {
    // ...现有字段...
    pools   map[string]*poolEntry // warmPoolID → 池
    poolsMu sync.RWMutex
    poolCfg PoolConfig            // 来自 adapter 配置（StateStore 类型、Redis 地址、默认并发等）
}

type poolEntry struct {
    pool       opensandbox.SandboxPool
    templateID string
    targetSize int
    createdAt  time.Time
}
```

方法实现（新建 `internal/adapter/opensandbox/warmpool.go`）：

- `CreateWarmPool(req)`：
  1. `templateID → imageURI` 复用 `CreateSandbox` 现有解析链（templateToImage → templates store → alias → 原样）
  2. `NewSandboxPoolBuilder().PoolName(id).MaxIdle(req.TargetSize).StateStore(store).ConnectionConfig(...).CreationSpec(PoolCreationSpec{Image: imageURI, Entrypoint: DefaultEntrypoint, ResourceLimits: DefaultResourceLimits, TimeoutSeconds: ...}).Build()`
  3. `Start(ctx)` 成功后写入 `a.pools`；已存在同名 → 409 语义错误
- `GetWarmPool(id)`：查 map → `Snapshot()`；`IdleCount→CurrentSize`、`MaxIdle→TargetSize`；`HealthState==Degraded → "degraded"`，`LifecycleState` 映射 `pending/ready/destroyed`
- `ListWarmPools()`：遍历 map 逐项 `Snapshot`
- `UpdateWarmPoolSize(id, n)`：`Resize(ctx, n)` 成功后更新 `targetSize`
- `DeleteWarmPool(id)`：`Shutdown(ctx, graceful=true)` 后从 map 删除（`Shutdown` 会释放 idle 沙箱）

**Acquire 接入**（改 `CreateSandbox`，`adapter.go` 中模板解析之后）：

```go
if entry := a.lookupPool(req.TemplateID); entry != nil {
    sbx, err := entry.pool.Acquire(ctx, opensandbox.AcquireOptions{...})
    if err == nil {
        // 走注册、返回，跳过直连创建
    }
    a.logger.Warn("pool acquire failed, falling back to direct create", ...)
}
// 现有 lifecycle.CreateSandbox 直连路径不变
```

Acquire 返回的 `*Sandbox` 已是 Running（SDK 内部做 ready 等待与健康检查），跳过 `waitRunning`。

**StateStore 选择**：配置 `pool.stateStore.type: memory|redis`；redis 时 `poolredis.NewRedisPoolStateStore(...)`。与现有 `templateStore` 的配置模式保持一致（复用 `buildTemplateStore` 的解析风格）。

**生命周期**：`SandboxAdapter` 接口没有 `Close()` 方法，新增会波及全部 4 个 adapter（超出本期范围）。决议：池与网关进程同生命周期；网关退出后遗留的池沙箱依赖服务端 `TimeoutSeconds`（`PoolCreationSpec` 建池时显式设置）自然过期回收。reconcile goroutine 随进程退出。

### 3.3 Data flow

```
POST /api/v1/warm-pools {templateID, targetSize}
        │
        ▼
CreateWarmPoolHandler ──► registry 选择 adapter
        │
   ┌────┴─────────────┐
   ▼                  ▼
agentsandbox        opensandbox
ExtensionsClient    PoolBuilder.Build()
.SandboxWarmPools   + Start(ctx)
(ns).Create(CRD)    + a.pools[id]=entry
   │                  │
   ▼                  ▼
K8s controller      reconcile loop
预热 replicas 个     预热 MaxIdle 个
Sandbox              sandbox（memory/Redis store）

POST /sandboxes {templateID}   （仅 opensandbox 有行为变化）
        │
        ▼
CreateSandbox: lookupPool(templateID)
        │命中                 │未命中/Acquire 失败
        ▼                     ▼
pool.Acquire()          lifecycle.CreateSandbox（现有路径）
```

## 4. API Surface

无新增端点。现有 5 个端点在两个后端上从 501/空变为真实实现：

| 端点 | 成功响应 |
|---|---|
| `GET /api/v1/warm-pools` | `[]WarmPoolInfo` |
| `POST /api/v1/warm-pools` | `WarmPoolInfo`（201） |
| `GET /api/v1/warm-pools/{id}` | `WarmPoolInfo` |
| `DELETE /api/v1/warm-pools/{id}` | 204 |
| `POST /api/v1/warm-pools/{id}/size` | `WarmPoolInfo` |

错误语义统一：404（`ErrWarmPoolNotFound`）、409（已存在）、400（targetSize<0、WarmPoolID 非法）。

> **实现注记**：现有 handler 采用"遍历所有 adapter、首个成功即返回"模式，不做逐错误映射（Get/Delete/Size 全失败 → 404，Create 全失败 → 503）。本期不改 HTTP 层，因此 409/400 暂不会透传到 HTTP 状态码；哨兵错误先在 adapter 层落地并保持测试覆盖，handler 级错误映射另开后续 issue。

## 5. Configuration

### agentsandbox

无必填新增。可选：

```yaml
backends:
  - name: agentsandbox
    config:
      warmPoolUpdateStrategy: OnReplenish   # 或 Recreate；默认 OnReplenish（CRD 默认值）
      maxWarmPoolSize: 256                  # TargetSize 上限，默认 256
      templateToSandboxTemplate:            # 可选：E2B templateID → SandboxTemplate ref 名
        my-template: my-sandbox-template    # 缺省时 templateID 直接作为 ref 名
```

解析需检查 `warmPoolUpdateStrategy` / `warmpoolupdatestrategy`、`templateToSandboxTemplate` / `templatetosandboxtemplate` 等双变体（Viper 小写化）。

### opensandbox

```yaml
backends:
  - name: opensandbox
    config:
      warmPool:                 # 注意：不用 "pool" —— 示例配置中 legacy "pool" 指 HTTP 连接池
        maxWarmPoolSize: 256
        stateStore:
          type: memory          # memory（默认）| redis
          # redis 时（与 templateStore 的 redis 字段风格一致）：
          addr: localhost:6379
          password: ""
          db: 0
          keyPrefix: "opensandbox:pool"
```

解析检查 `warmPool`/`warmpool`、`stateStore`/`statestore` 等双变体。`warmPool` 段缺省时 StateStore=memory，池功能仍可用。warmupConcurrency / reconcileInterval 本期用 SDK 默认值，不开放配置。

## 6. Security Considerations

- `WarmPoolID` 用户可控：agentsandbox 侧必须校验 K8s name 规范（`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`，≤63 字符），杜绝注入到 CRD name / label selector；opensandbox 侧校验同样规则并限制长度，避免作为 Redis key 前缀时产生歧义（`pool:{name}` 命名空间隔离由 SDK 处理，仍校验 `{`/`}`/空格等）
- `TargetSize` 上限：两侧统一 clamp 到配置的最大值 `maxWarmPoolSize`（默认 256），防止用户创建超大池耗尽集群/服务端资源（DoS）
- opensandbox `CreationSpec` 只来自服务端配置（templateToImage / templates store），**不接受**请求体传入 image/entrypoint，避免任意容器创建
- 权限沿用现有 `warm-pool:read` / `warm-pool:write` scope（`http.go:172-173`），无变化
- 日志不输出 Redis 密码、APIKey

## 7. Observability

- 结构化日志字段：`warmPoolID`、`templateID`、`adapter`、`targetSize`、`currentSize`
- opensandbox：`PoolLogger` 接口适配到 slog（Info/Warn/Debug）；Acquire 命中/回落各打一条带 `warmPoolID` 的日志；新增计数 `pool_acquire_total{result=hit|fallback|error}`（如现有 metrics 体系可用）
- agentsandbox：CRD 事件由 K8s 侧负责，网关只记 CRUD 操作日志

## 8. Testing Strategy

| 层 | 内容 |
|---|---|
| unit（agentsandbox） | 用 SDK 自带 fake extensions clientset 覆盖 5 方法 + Status 推导表驱动测试 + WarmPoolID 校验 |
| unit（opensandbox） | mock `lifecycleClient` + memory StateStore：Create/Get/List/Resize/Delete、重复创建 409、Acquire 命中与回落路径 |
| unit（共用） | `adapter.ErrWarmPoolNotFound` → handler 404 映射（如 handler 已有测试则补 case） |
| race | `go test -race ./internal/adapter/...`（池注册表并发读写） |
| E2E | mock adapter 行为基准对比；opensandbox 起本地 server 时验证建池→CreateSandbox 命中热池（池预热完成后创建延迟显著低于直连） |

## 9. Migration / Rollout

- 向后兼容：不建池时 opensandbox `CreateSandbox` 行为与现在完全一致（lookupPool 未命中即走原路径）
- agentsandbox 现有 `warmPoolName` / `templateToWarmPool` 消费方配置不变，新 CRUD 只是增加管理面
- 无 feature flag：stub → 实现是纯增量；旧版本对这两个后端调用 warm-pool API 得到 501，新版本得到真实结果
- 部署顺序无要求；agentsandbox 需要集群已安装 extensions CRD（`sandboxwarmpools`），未安装时 Create 返回明确错误信息（提示安装 CRD）

## 10. Open Questions

全部已决议（2026-11-12）：

| 问题 | 决议 |
|---|---|
| agentsandbox `TemplateID→TemplateRef` 映射方式 | ✅ 不反查 `TemplateToWarmPool`（语义错位）。Create 时 templateID 默认直接作 ref 名，经 `ExtensionsClient.SandboxTemplates(ns).Get` 校验存在性；可选配置 `templateToSandboxTemplate` 覆盖命名。Get/List 以 CRD `spec.sandboxTemplateRef.name` 为权威来源。建池成功后回写运行时 `warmPoolMap`，新池立即可被 `CreateSandbox` 消费（见 §3.2） |
| `TargetSize` 上限 | ✅ 默认 256（`maxWarmPoolSize` 可配） |
| opensandbox `DeleteWarmPool` 强制删除（`?force=true`） | ✅ 本期不做，单独开 issue 后续实现 |
