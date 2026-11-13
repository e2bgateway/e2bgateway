# OpenSandbox SDK v1.2.1 升级 + E2E 真实 server + examples 修复（#85 Phase 1）

**Issue**: #85 — [技术优化] 采用 OpenSandbox Go SDK v1.2.1：复用 SDK 能力瘦身 adapter，E2E 对齐 v1.2.1 并丰富用例
**Date**: 2026-11-13
**Priority**: P2
**Approach**: 分阶段 Phase 1 — 低风险项：SDK bump + E2E 换真实 server v1.2.1 并丰富用例 + examples 修复 + 死代码移除

---

## 1. Problem Statement

- `go.mod` 停在 SDK v1.2.0；v1.2.1 含多个与 gateway 直接相关的行为修复：pool acquire/drain 取消（上游 #2616/#2617，影响 `DeleteWarmPool` drain）、SSE leading BOM（#2618，影响 `ExecuteCodeStream`）、wake backoff budget（#2611）、status poll 保留 running-timeout 错误。
- opensandbox E2E 用 Python mock controller 模拟 Lifecycle + envd ConnectRPC 协议，无版本对齐声明，与 SDK v1.2.x 客户端预期可能漂移；#80/#84 的 warm pool force delete 零 E2E 覆盖。
- `examples/python/warm_pools.py` 字段名与 API 不符（`size` vs `targetSize`、`warmPoolId` vs `warmPoolID`），步骤 2-5 静默失败；无 force delete 演示；curl 示例无 warm-pools 段。
- 死文件：`test/kind-e2e/manifests/opensandbox/server-config.toml` 全仓零引用。

## 2. Design Goals

- SDK 升级至 v1.2.1，`make pre-commit` 与全量 `-race` 单测通过；重点回归 drain / streaming / status-poll 路径。
- `e2e-opensandbox` job 改为在 GitHub runner 宿主上运行**真实** OpenSandbox server v1.2.1（docker socket 模式，无 DinD）；mock controller 整体退役删除。
- 新增 E2E 用例：warm pool CRUD + **force delete**（idle 被杀、claimed 存活）、streaming、pause/resume、renew、ports 冒烟。
- examples：`warm_pools.py` 修复并含 force 演示；`curl/api_examples.sh` 增加 warm-pools 段。
- 删除死文件 `server-config.toml`（由新的版本化 E2E server 配置替代）。

**Non-goals（Phase 2 另行开 branch）：**

- `SandboxPoolManager.Destroy` / `EndpointCache` / `RetryConfig` 的采纳评估与 teardown 重构。
- examples 的 go / javascript warm pool 示例。
- agent-sandbox 侧任何变更（#86 追踪）。

## 3. Architecture

### 3.1 SDK bump

`go get github.com/alibaba/OpenSandbox/sdks/sandbox/go@v1.2.1 && go mod tidy`。v1.2.1 无导出 API 签名变更（纯行为修复），预期 drop-in；若编译暴露差异，逐项评估并保持 gateway 行为不回退。

### 3.2 E2E 真实 server 拓扑

```
GitHub runner (ubuntu)
├── docker: opensandbox/server:release-1.2.1   ← 挂 /var/run/docker.sock
│     ├── [runtime] type=docker → 沙箱 = 宿主兄弟容器 (execd image pinned)
│     ├── [proxy] resolve_internal=false, [docker] host_ip / port_range
│     └── env: OPENSANDBOX_INSECURE_SERVER=YES, SANDBOX_CONFIG_PATH
└── kind cluster
      └── gateway pod → baseURL http://172.17.0.1:8090 (宿主 docker bridge IP，
            Linux runner 上比 host.docker.internal 可靠)
```

- server 配置以官方 `server/docker-compose.example.yaml` 为模板，pin v1.2.1 配套镜像（server/execd/egress）。实现第一步用 `docker manifest inspect` 核实 `opensandbox/server:release-1.2.1` 与 execd/egress tag 实际存在；不存在则回退到最近的 release-1.2.x tag 并在 PR 说明。
- e2e job 步骤：拉起 server 容器 → health 等待 → 部署 kind + gateway（backend baseURL 指向 172.17.0.1:8090）→ 跑用例 → 失败时 dump server 容器日志（沿用现有 controller-logs 模式）。
- **删除** `test/kind-e2e/manifests/opensandbox/deployment.yaml`（Python mock controller 退役）；`server-config.toml` 重写为 v1.2.1 实际使用的版本化配置（路径不变，内容全新，不再是死文件）。

### 3.3 E2E 用例分级

- **核心集（PR 门禁）**：现有 43+ 用例中跑在 opensandbox backend 的部分 + 新增 warm pool CRUD/force delete + streaming 冒烟。
- **慢用例**：pause/resume、renew timeout、ports——用 `testing.Short()` 跳过开关控制，门禁默认跑核心集，nightly/手动可跑全量。
- force delete 用例断言：删除后 idle 沙箱在 server 侧被杀（List sandboxes 收敛）、已 claimed 的沙箱仍可操作。

### 3.4 examples

- `warm_pools.py`：`targetSize`（创建/更新）、`warmPoolID`（响应解析）、`targetSize`/`currentSize`（详情展示）；新增第 6 步 force delete（`requests.delete(..., params={"force": "true"})` + 注释说明 graceful/force 差异）。
- `curl/api_examples.sh`：新增 warm-pools 段（list / create / get / size / delete / delete?force=true）。

## 4. API Surface

无 gateway HTTP API 变更（force param 已由 #84 落地）。examples 与 E2E 消费既有 API。

## 5. Configuration

- 无 gateway 配置 schema 变更。
- E2E 专用 server 配置（`test/kind-e2e/manifests/opensandbox/server-config.toml`）：`[server] port=8090`、`[runtime] type=docker + execd_image pinned`、`[docker] host_ip/ port_range / drop_capabilities / no_new_privileges / pids_limit`、`[proxy] resolve_internal=false`、`[ingress] mode=direct`、egress image pinned。全部值以官方 compose 示例为基线。
- e2e gateway 配置（workflow 内联）：`baseURL: http://172.17.0.1:8090`，无 apiKey（INSECURE_SERVER 模式）。

## 6. Security Considerations

- 真实 server 以 `OPENSANDBOX_INSECURE_SERVER=YES` 运行——**仅限 CI 环境**，配置文件与 workflow 注释中显式标注不得用于生产。
- server 容器挂 docker.sock 拥有宿主 Docker 全权限：仅在 ephemeral GitHub runner 上运行；配置沿用官方示例的 `drop_capabilities` / `no_new_privileges` 收紧沙箱容器。
- 端口段 40000-60000 发布在 runner 宿主上，与 kind 端口映射不冲突（实现时核对 e2e.yml 现有端口占用）。
- examples 修复不涉及凭证处理变化。

## 7. Observability

- e2e job 失败时 dump：server 容器日志（tail -200）、`docker ps -a` 快照（沙箱兄弟容器状态）、gateway pod 日志（现有步骤保留）。
- 新 E2E 用例失败断言消息带 sandboxID / warmPoolID，便于对照 server 日志。

## 8. Testing Strategy

| 层 | 内容 |
|---|---|
| 单测 | 现有全量 `go test ./... -race` 作为 SDK bump 回归门槛；重点核对 opensandbox 包 drain/shutdown 相关既有测试（`TestDeleteWarmPool_*`）无行为漂移 |
| E2E（真实 server） | 核心集进 PR 门禁；warm pool force/graceful、streaming、pause/resume、renew、ports 按 §3.3 分级 |
| examples | `warm_pools.py` 与 curl 段对着本地或 CI 起的 gateway+server 手动跑通一遍（若本地无 Docker，则以 CI 验证为准并在 PR 说明） |
| 门槛 | `make pre-commit` 全绿 |

## 9. Migration / Rollout

- SDK bump 对 gateway 行为的影响限于 v1.2.1 的修复语义（drain 取消更及时、SSE BOM 容错、timeout 错误保留）——均为正向，无配置迁移。
- E2E mock 退役是一次性切换：mock 相关 manifest/脚本删除后，`test-auth-failures.sh opensandbox` 等共享脚本需在真实 server 下验证仍通过（auth failure 场景与 backend 类型弱相关，预期不变）。
- 无用户可见 breaking change。

## 10. Open Questions

| # | 问题 | Owner | 期限 |
|---|---|---|---|
| Q1 | ~~镜像 tag 是否存在~~ **已解决（2026-11-13）**：release BOM `1.2.1.yaml` 确认全套组件统一 tag `release-1.2.1`（server digest `sha256:07f9b39a…` 与本地已下载镜像一致；execd/egress 同为 `release-1.2.1`，注意**不是** `v1.2.1`） | 已核实 | 完成 |
| Q2 | 真实 server 下现有 43 个 e2b_api 用例的通过率与耗时（mock→real 行为差异面） | 实现者（先跑基线） | E2E 切换前 |
| Q3 | 172.17.0.1 在 GH runner kind 节点内的可达性（如不通，备选：kind extraPortMappings 或 host-gateway 显式映射） | 实现者 | workflow 改造时 |
