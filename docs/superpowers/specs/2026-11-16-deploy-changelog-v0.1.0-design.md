# Design: deploy 目录更新 + CHANGELOG 0.1.0（prometheus-operator 格式）

日期：2026-11-16
分支：`deploy-and-changelog-v0.1.0`（基于 `agentsandbox-v1.0.11`）

## 背景

- `deploy/` 自 2026-09-11（c358331）后未更新，落后于 warm pool、JWKS 认证、opensandbox
  TemplateStore、agent-sandbox v1.0.11 对齐、envd-only 数据面等功能变更。
- `CHANGELOG.md` 为 Keep a Changelog 格式，只有一个冗长的 `[Unreleased]` 节，遗漏 9 月中旬后的变更。
- 用户决定：全部内容（现有 + 新增）归入 `0.1.0` release；格式采用
  prometheus-operator CHANGELOG 风格。

## 第 1 部分：CHANGELOG.md 重写

- 文件直接以 `## 0.1.0 / 2026-11-16` 开头，无 Keep a Changelog 前言。
- 开头一个 `> **Note:**` 说明初始 release 定位。
- 条目为扁平列表：`* [TAG] 一句话描述 #PR`，TAG 取
  `[FEATURE] [ENHANCEMENT] [CHANGE] [BUGFIX] [SECURITY]`。
- 纯 dependabot / CI 修复类 commit 不列（prometheus-operator 惯例）。
- 旧 CHANGELOG 的冗长叙述压缩为单行条目，细节由 `docs/guides/` 承载。

条目来源（git log 核实）：#15 #42 #43 #44 #45 #46 #47 #48 #50 #59 #62 #63 #64 #80/#84 #82 #86 #87 #88。

## 第 2 部分：deploy 目录更新

kustomize base 与 helm chart 同步修复：

### 配置键修复

| 项 | 修复 | 依据 |
|---|---|---|
| opensandbox `endpoint` | → `baseURL`（必填键，原键导致启动失败） | `internal/adapter/opensandbox/factory.go:39-42` |
| helm `auth.methods` | → `auth.providers` | `internal/config/config.go:69-70` |
| agent-sandbox 无效键 | 删除 `kubeconfig` `sandboxClass` `defaultTemplate` `timeout` `sandboxIDAnnotation` | `agentsandbox/factory.go:42-57` 不读取 |
| helm 顶层 `portForwarding` | 删除（Config 无对应字段，viper 静默忽略） | `config.go:27-35` |
| helm `writeTimeout: 30s` | → `300s`（envd 长连接；代码默认 300s） | `config.go:231` |
| helm `routing.strategy: round-robin` | → `static`（合法值 static/template-based/weighted/priority） | `config.go:120` |
| helm `replicaCount: 3` | → `1` + 注释：envd token cache 与 sandbox→backend 映射为进程内存，多副本数据面 401 | `agentsandbox/adapter.go:250` |

### 新配置补齐

- warm pool：agent-sandbox `warmPoolName` `warmPoolUpdateStrategy` `maxWarmPoolSize`
  `templateToWarmPool` `templateToSandboxTemplate`；opensandbox `warmPool{maxWarmPoolSize,idleTimeout,stateStore}`。
- opensandbox `templateStore{type,...}`（memory/redis）。
- `server.envdDomain`（SDK host 路由依赖；缺省则仅 header 路径可用）。
- agent-sandbox `gatewayName` `gatewayNamespace` `apiURL`（sandbox-router tunnel 依赖）。
- 注释掉的 JWT provider 模板（静态 JWKS，`jwksURL` 明确不支持）。
- 键名/结构以 `configs/e2bgateway-example.yaml` 为参照。

### 端口清理（已核实）

- 9090 metrics 端口为死配置：代码中无独立监听、无 `/metrics` handler
  （仅 middleware 鉴权豁免）→ 从 kustomize deployment/service 与 helm 中删除。
- 8443/HTTPS：仅当 `server.https.{address,certFile,keyFile}` 齐全才监听 → 保留为注释示例。

### RBAC（最大缺口）

新增最小权限 ClusterRole + ClusterRoleBinding（kustomize base 与 helm 各一份）：

- `agents.x-k8s.io` / `sandboxes`：get,list,watch,create,delete,patch
- `extensions.agents.x-k8s.io` / `sandboxclaims`,`sandboxtemplates`,`sandboxwarmpools`：get,list,watch,create,update,patch,delete
- core / `pods`：get,list

### v1.0.11 对齐说明

helm `NOTES.txt` 与 README、kustomize README（如无则加注释块）说明前置依赖：
agent-sandbox v1.0.11 CRDs、controller、sandbox-router 安装（参照 `.github/workflows/e2e.yml:80-176`）。

## 验证

- `helm template` / `helm lint` 渲染通过。
- `kubectl kustomize deploy/kustomize/base` 渲染通过（如本机可用）。
- 配置键与 `configs/e2bgateway-example.yaml`、factory 源码逐一对照。

## 提交

两个 commit：`docs(changelog): rewrite for 0.1.0 in prometheus-operator format`、
`fix(deploy): align kustomize/helm with current config surface, add RBAC`。
