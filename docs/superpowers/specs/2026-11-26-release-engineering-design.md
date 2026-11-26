# 发布工程化：多架构镜像加固、SBOM、cosign 签名与版本注入

**Issue**: #120 — [v0.2.0][change] 发布工程化：多架构镜像、SBOM、cosign 签名与版本注入
**Date**: 2026-11-26
**Priority**: P2
**Approach**: 保留 buildx + 加固（已定稿，不迁移 ko / goreleaser）

---

## 1. Problem Statement

Issue 描述的部分现状已过时：`.github/workflows/release.yml` 的 docker job **已经**用
buildx 输出 linux/amd64 + linux/arm64（gateway 与 sandbox-base 两个镜像），带
`docker/metadata-action` OCI labels、semver tags 与 rc prerelease 处理；二进制已注入
`main.version`；`--version` flag 与 `version` 子命令已存在；helm chart 自动发布 GHCR OCI。

真正缺失的部分：

| 缺口 | 现状 |
|---|---|
| SBOM | 完全没有 |
| cosign 签名 | 完全没有 |
| commit 注入 | ldflags 只有 version/buildDate；Dockerfile 内用 `git describe`（build context 无 `.git` 时退化为 `dev`） |
| Dockerfile 交叉编译 bug | `ARG TARGETARCH` 声明了但编译未使用 `GOARCH`，arm64 构建靠 QEMU 模拟跑 Go 编译器 |
| `/versionz` 端点 | 只有 `/healthz` |
| Helm digest 固定 | values 只有 repository/tag |
| `:latest` tag | issue 要求但当前 metadata 不推 latest |
| 发布验证文档 | 无 |

## 2. Design Goals

- 两个镜像（gateway、sandbox-base）均有 SPDX SBOM（release asset）与 cosign keyless 签名
- 构建可复现：基础镜像 digest 固定、版本信息经 build-args 注入、真交叉编译
- `e2bgateway --version` / `version` 子命令输出 version/commit/buildDate；`GET /versionz` 暴露同等信息
- helm 支持 digest 固定部署；验证命令写入 docs
- rc 流程（v0.1.0-rc.x 先例）与现有 semver/helm 发布完全沿用，tag 触发全自动

**不做**（YAGNI）：ko/goreleaser 迁移、二进制 sign-blob、Makefile 本地 docker-build 改多架构。

## 3. 构建选型（已定稿）

**保留 buildx + 加固**。理由：

- release.yml 多架构已工作，迁移 ko 只覆盖 gateway（sandbox-base 内嵌 envd，必须 Dockerfile），管线分裂成两套
- goreleaser 引入大依赖，现有 changelog/helm/rc 流程全部重写，单 PR 风险高
- 可复现性通过 digest 固定基础镜像 + build-args 注入 + 交叉编译达成，无需换工具

## 4. 详细设计

### 4.1 Dockerfile 加固

- 新增 `ARG VERSION=dev`、`ARG COMMIT=unknown`、`ARG BUILD_DATE=unknown`；
  ldflags 注入 `-s -w -X main.version -X main.commit -X main.buildDate`，
  替代 Dockerfile 内的 `git describe` / `date`
- build stage 改 `FROM --platform=$BUILDPLATFORM golang:<tag>@sha256:...`，
  编译用 `GOOS=$TARGETOS GOARCH=$TARGETARCH` 交叉编译（buildx 自动注入 TARGET*），
  arm64 不再经 QEMU 模拟编译
- runtime stage `alpine:<tag>@sha256:...` digest 固定（实施时解析最新稳定 tag 的 digest）
- `GOPROXY` 改为 ARG，默认保留 `https://goproxy.cn,direct`，CI 可覆盖
- 保留现有静态 OCI labels；动态 labels（version/revision/created）由 metadata-action 注入
- sandbox-base 的 Dockerfile 不改构建方式（含 RUN 步骤，仍需 QEMU；QEMU action 保留）

### 4.2 版本注入（Go 侧）

- `cmd/e2bgateway/main.go`：新增 `commit = "unknown"` 变量，
  `cmd.NewRootCommand(version, commit, buildDate)`（签名变更）
- `internal/cmd/version.go`：输出 version/commit/buildDate/Go 版本；
  cobra `Version` 字段使 `--version` 自动生效
- 新增 `GET /versionz`：与 `/healthz` 一致免认证，返回
  `{"version": ..., "commit": ..., "buildDate": ..., "goVersion": ...}`；
  版本信息经 server 构造参数传入（与现有 config 注入路径一致）
- 同步更新 `docs/api/openapi.yaml`（CI 强制 openapi/路由表一致，见 #133）
- Makefile `LDFLAGS` 补 `-X main.commit=$(shell git rev-parse --short HEAD)`；
  release.yml 二进制构建补 commit 与 buildDate ldflags

### 4.3 release.yml：SBOM + cosign + latest

- workflow `permissions` 增加 `id-token: write`（keyless 签名必需）
- docker job 内，两个镜像各自 build-push 后取 action 输出的 **digest**：
  1. `anchore/sbom-action` 对 `repo@digest` 生成 SPDX JSON，
     命名 `e2bgateway_<version>_sbom.spdx.json` / `sandbox-base_<version>_sbom.spdx.json`，
     经 `softprops/action-gh-release` 附到同 tag 的 Release
  2. `sigstore/cosign-installer` + `cosign sign --yes repo@digest`
     （按 digest 签 manifest list，一次签名覆盖所有架构）
- gateway build 增加 `build-args: VERSION/COMMIT/BUILD_DATE`
  （COMMIT=`github.sha`，BUILD_DATE 取构建时刻 UTC）
- metadata tags 增加 `type=raw,value=latest,enable=${{ !contains(github.ref_name, '-') }}`
  （两个镜像都加；rc 不推 latest）
- rc prerelease、semver tags、changelog、helm-chart job 依赖关系全部不变

### 4.4 Helm digest 固定

- `values.yaml`：`image.digest: ""`（注释说明与 tag 互斥、digest 优先）
- `templates/deployment.yaml`：digest 非空渲染 `repo@sha256:...`，否则维持
  `repo:{{ tag | default .Chart.AppVersion }}`
- chart README 增加 digest 用法；`helm lint` + `helm template` 两种取值验证

### 4.5 发布验证文档

新增 `docs/guides/release-verification.md`：

- `cosign verify` keyless 命令（`--certificate-identity-regexp` 限定本 repo 的
  release workflow、`--certificate-oidc-issuer https://token.actions.githubusercontent.com`）
- SBOM 下载与用途说明
- digest 固定部署示例：`docker pull --platform linux/arm64 repo@sha256:...`、
  helm `--set image.digest=sha256:...`

## 5. Error Handling

- cosign / syft 步骤失败 → docker job 失败 → helm-chart job 不发布
  （现有 `needs: [release, docker]` 依赖已保证不会出现"chart 先于镜像"的漂移）
- `/versionz` 无外部依赖，纯内存信息，无错误路径
- Dockerfile build-args 缺省时回落 `dev`/`unknown`，本地 `docker build` 不破坏

## 6. Testing & Acceptance

- 单测：versionz handler（字段齐全、JSON 格式）、version 命令输出；`make pre-commit` 全绿
- 本地 `docker buildx build --platform linux/amd64,linux/arm64 .` 验证交叉编译产物架构正确
- `helm lint` / `helm template` 覆盖 digest 与 tag 两种渲染
- workflow 的 SBOM/签名步骤需真实 tag 触发才能端到端验证（可沿用 rc tag 先例）；PR 描述注明
- Issue 验收标准：`docker pull --platform linux/arm64` 可拉取；`cosign verify` 通过；
  SBOM 可在 release 页下载；`--version` 输出准确

## 7. Files Touched

| 文件 | 变更 |
|---|---|
| `Dockerfile` | build-args、交叉编译、digest 固定、GOPROXY ARG |
| `cmd/e2bgateway/main.go` | commit 变量 |
| `internal/cmd/root.go` / `version.go` | 签名变更、输出增强 |
| `internal/api/v1/`（health/version handler） | 新增 VersionHandler |
| `internal/server/http.go`（及 server 构造） | 注册 `/versionz`、传入版本信息 |
| `docs/api/openapi.yaml` | `/versionz` 路径 |
| `Makefile` | LDFLAGS 补 commit |
| `.github/workflows/release.yml` | id-token、SBOM、cosign、latest、build-args、二进制 ldflags |
| `deploy/helm/e2bgateway/values.yaml` / `templates/deployment.yaml` / `README.md` | digest 支持 |
| `docs/guides/release-verification.md` | 新增 |