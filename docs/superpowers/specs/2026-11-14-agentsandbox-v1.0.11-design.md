# agent-sandbox v1.0.11 对齐：SDK/E2E 升级、legacy 数据面收敛、E2E 用例丰富

**Issue**: #86 — [技术优化] 采用 agent-sandbox v1.0.11：SDK/E2E 版本对齐、legacy 代码收敛、E2E 用例丰富
**Date**: 2026-11-14
**Priority**: P2
**Approach**: 全范围单 branch——SDK bump + E2E manifests/镜像对齐 v1.0.11 + 彻底删除 legacy SDK handle 数据面 + agent-sandbox job 启用 warm pool 活体段并新增 gateway 重启恢复用例 + examples README backend 配置段

---

## 1. Problem Statement

- `go.mod` 停在 `sigs.k8s.io/agent-sandbox v1.0.10`，上游已发布 **v1.0.11**（含 controller/SDK/router 修复）。
- E2E 版本严重漂移：`e2e.yml` 部署的 CRD/controller manifests 与镜像是 **v1.0.2**（落后 SDK 9 个版本），controller image 还有一处硬编码 patch；sandbox-router 用 **`latest-main` 移动目标**，E2E 结果不可复现。
- agentsandbox adapter 双维护两条数据面：envd ConnectRPC（默认）与标注 `(legacy)` 的 SDK handle 路径（10 个 `*ViaHandle` 函数 + 11 处 `useEnvdDataPlane` 分支 + 配置开关），后者依赖 agent-sandbox runtime sidecar，E2E 从不覆盖。
- #80/#84 落地的 warm pool force delete 在 agent-sandbox backend 上零活体 E2E；template→pool 路由的跨重启恢复（annotation restore）只有单测。
- `examples/README.md` 无任何 backend 配置说明。

## 2. Design Goals

- SDK 升级至 v1.0.11，`make pre-commit` 与全量 `-race` 单测通过；`TestWarmPoolNameHash` 继续 pin 住与上游 `controllers.NameHash` 的兼容性（force delete 的 label 选择依赖它）。
- E2E 的 CRD/controller manifests、controller 镜像、sandbox-router 镜像全部对齐 **v1.0.11**，消灭 `latest-*` 移动目标。
- 彻底删除 legacy SDK handle 数据面：envd 成为 agentsandbox 唯一数据面；`useEnvdDataPlane` 配置项移除（breaking，PR 与 AGENTS.md 标注）。
- agent-sandbox E2E job 启用现有 warm pool 活体段（`WARMPOOL_E2E=1`），并新增 gateway 重启后路由恢复用例（`RESTART_E2E=1` 门控）。
- `examples/README.md` 增加 agent-sandbox backend 配置示例段。

**Non-goals：**

- `GetPortURL`/`ListPorts` 与 SDK portforward/gateway 的重构对齐——评估结论为**不采纳**：现实现基于 Pod IP 直连，在集群内工作正常；v1.0.11 router 的 path-prefix 能力（#2203）面向浏览器直连场景，gateway 无此需求。
- SDK v1.0.11 新能力采纳（`Options.SandboxdAuthToken`、`Files.List` recursive 等）——gateway 走 envd 数据面，无消费方。
- opensandbox adapter 任何变更。
- pause/resume Ready 语义、ports 经 router 的全套 E2E（CI 时长与不稳定风险；语义由单测覆盖）。

## 3. Architecture

### 3.1 SDK bump（v1.0.10 → v1.0.11）

`go get sigs.k8s.io/agent-sandbox@v1.0.11 && go mod tidy`。v1.0.11 相关变更的影响评估：

| 上游变更 | 影响 | 动作 |
|---|---|---|
| #2021 Pod terminating 时 Sandbox `Ready=False`（reason `PodTerminating`） | `warmPoolStatus` 的 ready 判定与 claim 就绪语义更准确（terminating pod 不再虚报 ready） | 无代码变更；全量回归 + E2E warmup 等待路径实证 |
| #2182 Retain shutdown policy 的 warm assignment 清理 | controller 侧修复，降低池成员泄漏 | 无 gateway 变更 |
| #2206 `Options.SandboxdAuthToken`（`X-Sandboxd-Token`） | gateway 数据面是 envd（49983），不经 sandboxd | 不采纳（Non-goal） |
| Go SDK `Files.List` `WithRecursive/WithMaxDepth`、`Files.Write` `WithAppend/WithCreateOnly` | 仅 SDK handle 路径消费——该路径本 branch 删除 | 不采纳 |
| #2203 router path-routing 响应重写（默认开启） | E2E router 升级后行为变化：Set-Cookie path / Location 重写。gateway 经 router 的 tunnelStrategy 流量为 header 路由，不受 path 前缀重写影响 | E2E 实证；如出现兼容问题，router args 加 `--path-routing-rewrite-responses=false` 回退 |
| `controllers.NameHash` | force delete 的 label 值来源 | 升级后核对 v1.0.11 源码 hash 实现未变；`TestWarmPoolNameHash` pin |

### 3.2 E2E 版本对齐（e2e.yml `e2e-agent-sandbox` job）

- `AGENT_SANDBOX_VERSION=v1.0.2` → `v1.0.11`：CRD、controller、extensions controller、RBAC manifests 的 raw URL 随变量自动对齐。
- controller image patch：硬编码 `registry.k8s.io/agent-sandbox/agent-sandbox-controller:v1.0.2` → 用 `${AGENT_SANDBOX_VERSION}` 渲染，单一版本来源。
- sandbox-router：`us-central1-docker.pkg.dev/k8s-staging-images/agent-sandbox/sandbox-router-go:latest-main` → 首选 `registry.k8s.io/agent-sandbox/sandbox-router-go:v1.0.11`。本机无法验证该镜像是否已 promote（registry 不可达）；**CI 首跑核实**：若 pull 失败，回退 staging registry 并将 `latest-main` 解析为当时的不可变 digest pin（注释记录解析命令与日期）。
- v1.0.2 → v1.0.11 跨版本风险（CRD schema、controller 行为、extensions API）由 E2E 全量用例实证；失败则按日志逐项迭代（每次修复独立 commit）。

### 3.3 legacy SDK handle 数据面删除

删除清单（`internal/adapter/agentsandbox/`）：

- 10 个 `*ViaHandle` 方法：`executeCodeViaHandle`、`runCommandViaHandle`、`writeFileViaHandle`、`readFileViaHandle`、`uploadFileViaHandle`、`downloadFileViaHandle`、`listFilesViaHandle`、`makeDirViaHandle`、`removeFileViaHandle`、`setEnvsViaHandle`
- 11 处 `if a.useEnvdDataPlane { ... } return ...ViaHandle(...)` 分支——保留 envd 分支主体，去掉条件
- `Adapter.useEnvdDataPlane` 字段、`AdapterConfig.UseEnvdDataPlane`、factory 的 `useenvddataplane` 解析
- 仅被 legacy 路径引用的 helper（`getHandle` 等）按引用分析连带删除；`a.client`（`sandbox.Client`）仍被 CreateSandbox/ListAllSandboxes 等使用，保留
- 相关单测中仅覆盖 handle 路径的用例删除；envd 路径用例保留

**Breaking change**：配置了 `useEnvdDataPlane: false` 的部署升级后配置键被忽略（factory 对未知键不报错），数据面静默切换为 envd。PR 描述 breaking 段 + AGENTS.md 明示：envd 是唯一数据面，sandbox 镜像必须内嵌 envd（`images/sandbox/`）。

AGENTS.md 同步：adapter 表中数据面描述去掉 dual-path 措辞；配置说明移除 `useEnvdDataPlane`。

### 3.4 E2E 用例丰富（agent-sandbox job）

- **`WARMPOOL_E2E=1`**：复用现有 runner 活体段（force 段：create→预热→claim 证据（idle 计数下降）→force delete→池 404→claimed 存活可执行命令；graceful 段：同构 + graceful 删除）。agentsandbox 的 force delete（Background propagation + 按 `warmPoolNameHash` label 直删 unclaimed + UID/RV preconditions）由此获得活体覆盖。前置条件已具备：job 已创建 `base` SandboxTemplate；extended 段的 pause 走 CRD patch，行为由既有 skip-on-fail 逻辑兜底。
- **`RESTART_E2E=1` 新增重启恢复段**（仅 agent-sandbox job）：
  1. API 创建池 `wp-e2e-restart`（template `base`，targetSize 1），等待 `currentSize ≥ 1`
  2. `kubectl -n e2bgateway-system rollout restart deployment/e2bgateway` + `rollout status` + healthz 等待
  3. 创建沙箱（templateID `base`）
  4. `kubectl get sandboxclaims -n <ns> -o json` 断言存在 `spec.warmPoolRef.name == wp-e2e-restart` 的 claim——证明 template→pool 路由经 annotation restore 跨重启恢复
  5. 清理：删沙箱、force delete 池
  - runner 脚本以 env 门控（opensandbox job 不设：其池定义是进程内的，重启即失，语义不适用）
  - 重启会重建 gateway Pod：脚本对 healthz 的等待沿用现有 retry 循环

### 3.5 examples/README.md

新增 "Backend configuration (agent-sandbox)" 段：完整 YAML 片段（`type: agent-sandbox`、`namespace`、`templateToSandboxTemplate`、`templateToWarmPool`、`warmPoolName`、`warmPoolUpdateStrategy`、`maxWarmPoolSize`），不再出现 `useEnvdDataPlane`；注明 sandbox 镜像需内嵌 envd（指向 `images/sandbox/`）。

## 4. API Surface

无 gateway HTTP API 变更。行为面唯一对外变化：`useEnvdDataPlane` 配置键失效（§3.3 breaking 标注）。

## 5. Configuration

- **移除**：`backends[].config.useEnvdDataPlane`（agentsandbox factory 不再解析；设置该键无效果）。
- E2E 侧：`AGENT_SANDBOX_VERSION` 变量升为 v1.0.11；runner 新增 `RESTART_E2E` 门控变量（与 `WARMPOOL_E2E` 同处 export）。
- `configs/e2bgateway-example.yaml`：如有 `useEnvdDataPlane` 示例行则删除（现无则不动）。
- Viper 小写化注意：删除的解析本就同时接受 `useenvddataplane`/`useEnvdDataPlane`，移除后两种写法都失效。

## 6. Security Considerations

- legacy 删除缩小攻击面：`removeFileViaHandle` 等路径含 shell 拼接（`util.ShellQuote` 保护），删除后该面消失；envd 路径为结构化 ConnectRPC 调用，无 shell 注入面。
- router `latest-main` → 版本 pin 消除供应链移动目标（不可复现的镜像内容）。
- v1.0.11 sandboxd 安全加固（#2218 symlink 删除、#2222 FIFO 防护、#2225 PTY EOF）随 E2E 镜像升级自动获益（gateway 不直接消费 sandboxd）。
- RESTART_E2E 用例只操作 CI 集群内资源，无新增权限。

## 7. Observability

- legacy 分支删除后日志面收窄（handle 路径的 warn/error 消失）；envd 路径日志不变。
- RESTART_E2E 失败时 runner 输出 rollout 状态与 claim JSON 摘要（沿用 pass/fail 消息机制），e2e.yml 诊断段已有 gateway 日志 dump。

## 8. Testing Strategy

| 层 | 内容 |
|---|---|
| 单测（-race） | SDK bump 后全量回归（clients/go API 无签名变化预期；有则逐项评估）；legacy 删除后编译期即验证无残留引用；`TestWarmPoolNameHash` pin hash；既有 warm pool/routing 单测（`TestRestoreWarmPoolRouting_*` 等）覆盖重启恢复语义 |
| E2E（agent-sandbox job） | v1.0.11 manifests/controller/router 下全量既有用例 + `WARMPOOL_E2E=1` 活体段（force/graceful）+ `RESTART_E2E=1` 重启恢复段 |
| E2E（opensandbox job） | 不受影响，全量跑作为回归 |
| 门槛 | `make pre-commit` 全绿 |

## 9. Migration / Rollout

- **Breaking**：`useEnvdDataPlane: false` 的部署升级后静默切到 envd。迁移路径：sandbox 镜像内嵌 envd（`images/sandbox/Dockerfile` 模式）。release note + PR 描述 + AGENTS.md 三处标注。
- v1.0.2 → v1.0.11 controller 升级仅影响 E2E 集群（生产部署的 controller 版本由使用方自管；gateway 代码兼容 v1.0.x extensions API——以 E2E 实证）。
- SDK bump 无配置迁移。

## 10. Open Questions

| # | 问题 | Owner | 期限 |
|---|---|---|---|
| Q1 | `registry.k8s.io/agent-sandbox/sandbox-router-go:v1.0.11` 是否已 promote（本机 registry 不可达无法核实） | 实现者（CI 首跑；失败则 staging digest pin 回退，方案见 §3.2） | workflow 改造首跑 |
| Q2 | v1.0.2 → v1.0.11 CRD schema 漂移是否影响既有 E2E 断言（如 SandboxClaim/SandboxWarmPool 字段） | 实现者（E2E 全量跑实证） | E2E 对齐首跑 |
| Q3 | RESTART_E2E 的 claim 断言细节：gateway 创建 claim 的 namespace 与 `warmPoolRef` 字段路径（`spec.warmPoolRef.name`，以 v1.0.11 extensions API 为准） | 实现者（编码前读 SDK types 确认） | Task 编码前 |
