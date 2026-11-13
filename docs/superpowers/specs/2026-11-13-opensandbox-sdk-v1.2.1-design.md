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

### 3.2 E2E 真实 server 拓扑（K8s 部署）

真实 OpenSandbox server **部署在 kind 集群内**（官方 Helm umbrella chart），沙箱为集群内 Pod：

```
GitHub runner (ubuntu)
└── kind cluster
      ├── opensandbox-system ns
      │     ├── CRDs + RBAC          (chart: base)
      │     ├── opensandbox-controller (chart: controller, 管理沙箱 Pod/BatchSandbox)
      │     └── opensandbox-server   (chart: server, Service 名固定 opensandbox-server:80)
      ├── opensandbox ns             (沙箱工作负载 namespace，chart 不创建，需预建)
      └── e2bgateway-system ns
            └── gateway pod → baseURL http://opensandbox-server.opensandbox-system/v1
```

- 安装方式：CI 中 shallow clone `alibaba/OpenSandbox` 的 `release-1.2.1` tag → `helm dependency build manifests/charts/opensandbox` → `helm install`（chart 默认镜像即 `release-1.2.1`，registry 默认 aliyun 镜像仓库，GH runner 与本地均可达）。可选覆盖值放 `test/kind-e2e/manifests/opensandbox/values-e2e.yaml`（版本化管理）。
- server 的 `[runtime] type=kubernetes` 由 chart 默认 `configToml` 提供（execd_image 已 pin release-1.2.1）；沙箱 Pod 由 controller 经 BatchSandbox 模板创建。
- **ingress-gateway 不启用**：官方文档的 "REQUIRED" 针对集群外客户端；e2bgateway 与沙箱同集群，`[ingress] mode=direct` 返回的 Pod IP 在 kind 扁平网络内可直达。若 CI 首跑证明数据面不通，再启用 ingress-gateway 子 chart 并配置 `opensandbox-server.server.gateway.*`。
- **删除** `test/kind-e2e/manifests/opensandbox/deployment.yaml`（Python mock 退役）与 `server-config.toml`（docker-runtime 时代的死文件；K8s 模式下 server 配置由 chart values 管理，替代物为 `values-e2e.yaml`）。

### 3.3 E2E 用例分级

- **核心集（PR 门禁）**：现有 runner 用例 + 新增 warm pool CRUD/force delete 活体用例。
- **慢用例**：renew timeout、ports、pause/resume——由 `WARMPOOL_E2E=1` 门控（仅 opensandbox job 开启）。**pause/resume 在 K8s 模式依赖 snapshot 基础设施**（image-committer + 可推送的镜像 registry，chart 默认 `registry: ""`），未配置时 pause 会失败：用例做**尽力而为**处理——非 2xx 时记 skip（带原因）而非 fail，配置本地 registry 后再升级为 fail 断言（后续 issue）。
- force delete 用例断言：删除后池 404、已 claimed 的沙箱仍可执行命令（idle 沙箱被杀由 server 侧 drain 保证，活体断言以池消失 + claimed 存活为准，避免依赖沙箱兄弟 Pod 的列表时序）。
- 活体用例载体是 `hack/kind-e2e/run-examples-e2e.sh`（bash/curl，现有活体 E2E 均在此）；`test/e2e/*.go` 为 mock-backend 形状测试，不变。

### 3.4 examples

- `warm_pools.py`：`targetSize`（创建/更新）、`warmPoolID`（响应解析）、`targetSize`/`currentSize`（详情展示）；新增第 6 步 force delete（`requests.delete(..., params={"force": "true"})` + 注释说明 graceful/force 差异）。
- `curl/api_examples.sh`：新增 warm-pools 段（list / create / get / size / delete / delete?force=true）。

## 4. API Surface

无 gateway HTTP API 变更（force param 已由 #84 落地）。examples 与 E2E 消费既有 API。

## 5. Configuration

- 无 gateway 配置 schema 变更。
- E2E 专用 helm 覆盖值：`test/kind-e2e/manifests/opensandbox/values-e2e.yaml`（最小覆盖——沙箱工作负载 namespace、日志级别等；镜像 tag 用 chart 默认 release-1.2.1）。server 配置（`[runtime] type=kubernetes`、execd_image pin）由 chart 默认 `configToml` 提供。
- e2e gateway 配置（workflow 内联）：`baseURL: http://opensandbox-server.opensandbox-system/v1`（Service 名由 chart `fullnameOverride: opensandbox-server` 固定，端口 80），无 apiKey（server `api_key = ""`，仅 CI）。

## 6. Security Considerations

- K8s 模式**不挂 docker.sock**（对比宿主 docker 方案的显著改善）：server/controller 以 Pod 运行，权限由 chart RBAC 约束。
- server `api_key = ""`（无鉴权）——**仅限 ephemeral CI 集群**，values-e2e.yaml 与 workflow 注释显式标注不得用于生产。
- 沙箱 Pod 的 execd 注入与 capability 收紧由官方 chart/模板负责，随 release-1.2.1 版本化。
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
| Q2 | ~~43 个 e2b_api 用例真实 server 基线~~ **已澄清**：`test/e2e/*.go` 是 mock-backend 形状测试，不在真实后端上跑；活体用例载体是 runner 脚本 | 已核实 | 完成 |
| Q3 | ~~172.17.0.1 可达性~~ **已失效**（K8s 部署后 gateway 走集群内 Service DNS）。替代未知项：(a) kind 预载镜像 `sandbox-with-envd:local` 在 controller 沙箱 Pod 模板下的 imagePullPolicy（若 Always 需本地 registry 方案）；(b) BatchSandbox 模板在 kind 单节点上的资源满足性 | 实现者（本地 kind + CI 首跑核实） | E2E 切换时 |
| Q4 | pause/resume 的 snapshot registry（image-committer 推送目标）——CI 未配置时用例按 skip 处理 | 后续 issue | 非本期 |
