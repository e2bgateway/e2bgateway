# agent-sandbox v1.0.11 对齐实现计划（#86）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** SDK 与 E2E 全部对齐 agent-sandbox v1.0.11（消灭 v1.0.2 manifests 与 `latest-main` router 移动目标），彻底删除 legacy SDK handle 数据面，agent-sandbox E2E 启用 warm pool 活体段并新增 gateway 重启路由恢复用例，examples README 补 backend 配置段。

**Architecture:** 变更集中在 `internal/adapter/agentsandbox/`（删 legacy）、`.github/workflows/e2e.yml`（版本对齐 + env gate）、`hack/kind-e2e/run-examples-e2e.sh`（RESTART_E2E 段）、docs/examples。opensandbox 侧零变更。

**Tech Stack:** Go 1.26、sigs.k8s.io/agent-sandbox v1.0.11、kind、kubectl、GitHub Actions。

**Spec:** `docs/superpowers/specs/2026-11-14-agentsandbox-v1.0.11-design.md`

## Global Constraints

- CI 门槛：`make pre-commit` 全绿；commit 一律 `git commit -s`（DCO）。
- **Breaking 已批准**：`useEnvdDataPlane` 配置键删除（spec §3.3），PR 描述与 AGENTS.md 必须标注。
- envd 成为 agentsandbox 唯一数据面；`a.client`（`sandbox.Client`，用于 claim 生命周期/ListAllSandboxes/GetSandbox/DeleteSandbox）**保留**。
- `warmPoolNameHash` 本地复刻保留（避免 controller-runtime/prometheus 依赖）；bump 后必须核对 v1.0.11 的 `controllers.NameHash` 实现未变（`TestWarmPoolNameHash` pin 值 `wp-1 → a389afb4`、`my-pool → 244dc158`）。
- router 镜像 pin 策略：首选 `registry.k8s.io/agent-sandbox/sandbox-router-go:v1.0.11`；CI pull 失败则回退 `us-central1-docker.pkg.dev/k8s-staging-images/agent-sandbox/sandbox-router-go` 并 pin 当时 `latest-main` 的不可变 digest（注释记录解析命令与日期）。
- 每 task 一个 commit（特殊说明除外），conventional commits + `-s`。

---

### Task 1: SDK bump v1.0.10 → v1.0.11

**Files:**
- Modify: `go.mod`、`go.sum`

**Interfaces:**
- Produces: 全仓编译/测试基于 v1.0.11；后续 task 的基线。

- [ ] **Step 1: 升级**

```bash
go get sigs.k8s.io/agent-sandbox@v1.0.11
go mod tidy
grep -n "sigs.k8s.io/agent-sandbox" go.mod   # 确认 v1.0.11
```

- [ ] **Step 2: NameHash 兼容性核对（Global Constraint）**

```bash
grep -n "func NameHash" -A 14 "$(go env GOMODCACHE)/sigs.k8s.io/agent-sandbox@v1.0.11/controllers/sandbox_controller.go"
```

Expected: 仍是 FNV-1a → 8 位小写 hex（与 `warmPoolNameHash` 一致）。若算法变了：停下，同步改 `warmPoolNameHash` 与 `TestWarmPoolNameHash` 的 pin 值（用新算法重算）。

- [ ] **Step 3: 编译 + 全量回归**

Run: `go build ./... && go vet ./...`
Expected: 无错误。若 v1.0.11 有 API 变化导致编译失败，逐项评估适配，不得回退 gateway 行为。

Run: `go test ./... -race`
Expected: 全 PASS（重点：`TestWarmPoolNameHash`、`TestDeleteWarmPool_Force*`、`TestRestoreWarmPoolRouting_*`）。

- [ ] **Step 4: pre-commit + commit**

```bash
make pre-commit   # exit 0
git add go.mod go.sum
git commit -s -m "chore(deps): bump sigs.k8s.io/agent-sandbox to v1.0.11"
```

---

### Task 2: 删除 legacy SDK handle 数据面

**Files:**
- Modify: `internal/adapter/agentsandbox/adapter.go`
- Modify: `internal/adapter/agentsandbox/factory.go:51-52`
- Modify: `internal/adapter/agentsandbox/integration_test.go:579-690`
- Modify: `AGENTS.md`（数据面描述，与 Task 5 合并提交亦可，但本 task 内先改 adapter 表行）
- Test: `internal/adapter/agentsandbox/*_test.go`

**Interfaces:**
- Consumes: Task 1 的 v1.0.11 基线。
- Produces: envd 单数据面；`useEnvdDataPlane`/`UseEnvdDataPlane`/`*ViaHandle`/`getHandle` 符号全部消失。后续 task 依赖"编译通过即无残留引用"。

**已核实事实**（行号以 Task 1 完成后的 HEAD 为准，实施时用 grep 重新定位）：
- 11 处 `if a.useEnvdDataPlane` 分支，所在方法：`ExecuteCode`(487)、`RunCommand`(551)、`WriteFile`(616)、`ReadFile`(642)、`UploadFile`(682)、`DownloadFile`(712)、`ListFiles`(743)、`MakeDir`(795)、`RemoveFile`(822)、`SetEnvs`(1442)、`MoveFile`(1511)
- 11 个 `*ViaHandle` 方法：`executeCodeViaHandle`(514)、`runCommandViaHandle`(587)、`writeFileViaHandle`(632)、`readFileViaHandle`(668)、`uploadFileViaHandle`(698)、`downloadFileViaHandle`(728)、`listFilesViaHandle`(772)、`makeDirViaHandle`(811)、`removeFileViaHandle`(838)、`setEnvsViaHandle`(1467)、`moveFileViaHandle`（MoveFile else 分支调用，grep 定位）
- `getHandle`(923) 仅被 `*ViaHandle` 调用 → 连带删除；`resolveClaimName` 被 `getHandle` 和 `resolveSandboxCRName` 共用 → **保留**
- 测试引用：`integration_test.go:621`（`useEnvdDataPlane: true` 字段初始化）、`TestSetEnvs_RoutesViaHandle`(658-690，整个删除)

- [ ] **Step 1: 删除分支——11 个方法体收敛为 envd 单路径**

每个方法的模式变换（以 `ExecuteCode` 为例，其余 10 个同构）：

```go
// 变换前
func (a *Adapter) ExecuteCode(ctx context.Context, sandboxID string, req *adapter.CodeExecutionRequest) (*adapter.CodeExecutionResult, error) {
	if a.useEnvdDataPlane {
		return a.executeCodeViaEnvd(ctx, sandboxID, req)
	}
	return a.executeCodeViaHandle(ctx, sandboxID, req)
}

// 变换后
func (a *Adapter) ExecuteCode(ctx context.Context, sandboxID string, req *adapter.CodeExecutionRequest) (*adapter.CodeExecutionResult, error) {
	return a.executeCodeViaEnvd(ctx, sandboxID, req)
}
```

同时删除 `SetEnvs` 上方提及 dual-path 的注释块（"envd data plane like the other data-plane operations: when useEnvdDataPlane is true…"），改为单句："SetEnvs appends variables to /etc/environment via the envd data plane."

- [ ] **Step 2: 删除 11 个 `*ViaHandle` 方法与 `getHandle`**

逐个删除（含各自的 doc comment）。删除后核查孤儿 helper：

```bash
go build ./internal/adapter/agentsandbox/ 2>&1 | head   # 未使用即编译错（Go 对未使用方法不报错，用 grep 核对）
for fn in getHandle executeCodeViaHandle runCommandViaHandle writeFileViaHandle readFileViaHandle uploadFileViaHandle downloadFileViaHandle listFilesViaHandle makeDirViaHandle removeFileViaHandle setEnvsViaHandle moveFileViaHandle; do
  grep -rn "$fn" internal/ && echo "LEFTOVER: $fn"
done
```

Expected: 无 LEFTOVER 输出。再检查仅被 handle 路径使用的 import（如 `sandbox.Handle` 相关）与工具函数（`util.ShellQuote` 若仅 removeFileViaHandle 使用则 import 清理；`util` 包本身仍被 factory 用）。

- [ ] **Step 3: 删除字段与配置**

- `adapter.go`：删 `useEnvdDataPlane bool` 字段（含注释，~L136-139）、`AdapterConfig.UseEnvdDataPlane`（含注释，~L191-193）、`New()` 中 `useEnvdDataPlane: cfg.UseEnvdDataPlane,`（~L258）
- `factory.go:51-52`：删

```go
	// UseEnvdDataPlane defaults to true for agent-sandbox backend
	cfg.UseEnvdDataPlane = boolVal(raw, true, "useenvddataplane", "useEnvdDataPlane")
```

（若 `boolVal` helper 因此无其他调用方则一并删除——grep 确认。）

- [ ] **Step 4: 测试清理**

- `integration_test.go`：删除 `TestSetEnvs_RoutesViaHandle` 整个函数（658-690）；L579-653 的 SetEnvs envd 测试中删去 `useEnvdDataPlane: true` 字段行与注释里的 dual-path 措辞
- 全仓扫尾：

```bash
grep -rn "useEnvdDataPlane\|UseEnvdDataPlane\|ViaHandle" internal/ test/ configs/ AGENTS.md docs/api examples/ | grep -v docs/superpowers
```

Expected: 仅剩 docs/superpowers 下的历史 spec/plan（历史记录不改）。configs/ 与 AGENTS.md 若有命中，本 step 一并清理（AGENTS.md adapter 表 agent-sandbox 行去掉 dual-path 描述，改为 "Data plane: envd ConnectRPC (embedded in the sandbox image); the legacy SDK-handle path was removed in v1.0.11 alignment (#86)"）。

- [ ] **Step 5: 回归 + commit**

```bash
go build ./... && go test ./internal/adapter/agentsandbox/ -race
make pre-commit   # exit 0
git add -A
git commit -s -m "refactor(agentsandbox)!: remove legacy SDK-handle data plane; envd is the only path

BREAKING CHANGE: the useEnvdDataPlane backend config key is removed and
ignored. Sandboxes must embed envd (see images/sandbox/). The legacy path
depended on the agent-sandbox runtime sidecar and was never covered by E2E."
```

---

### Task 3: E2E 对齐 v1.0.11（manifests / controller / router）

**Files:**
- Modify: `.github/workflows/e2e.yml`（`e2e-agent-sandbox` job，约 L78-190）

**Interfaces:**
- Produces: E2E 集群运行 v1.0.11 CRD/controller/router；Task 4 的用例在此环境跑。

- [ ] **Step 1: 版本变量与 controller 镜像**

- `AGENT_SANDBOX_VERSION=v1.0.2` → `AGENT_SANDBOX_VERSION=v1.0.11`
- image patch 的 `"value": "registry.k8s.io/agent-sandbox/agent-sandbox-controller:v1.0.2"` → `"value": "registry.k8s.io/agent-sandbox/agent-sandbox-controller:${AGENT_SANDBOX_VERSION}"`（注意该 patch 在双引号 json patch 字符串内，确认 shell 展开生效——`kubectl patch ... -p='[...]'` 单引号会阻止展开，需改为双引号包裹 JSON 并转义内部引号，或先 `PATCH=$(cat <<EOF ... EOF)` 用 heredoc 展开变量再传入）

- [ ] **Step 2: router 镜像 pin**

`sandbox-router-go:latest-main` → `registry.k8s.io/agent-sandbox/sandbox-router-go:v1.0.11`，Deployment YAML 上方注释改为：

```yaml
          # Pinned to the release tag (no moving latest-main target, #86).
          # If registry.k8s.io has not promoted this tag yet, fall back to the
          # staging registry pinned by digest:
          #   docker buildx imagetools inspect us-central1-docker.pkg.dev/k8s-staging-images/agent-sandbox/sandbox-router-go:latest-main
          # then use .../sandbox-router-go@sha256:<digest> and record the date.
```

- [ ] **Step 3: gateway config 移除 templateToWarmPool 覆盖**

`e2e-agent-sandbox` job 的 gateway ConfigMap 中：

```yaml
                    warmPoolName: base
                    templateToWarmPool:
                      "base": "base"
```

改为（保留 warmPoolName，删 templateToWarmPool 并注释原因）：

```yaml
                    warmPoolName: base
                    # No templateToWarmPool override: config entries always win
                    # over CRUD-created pools, which would mask the warm pool
                    # live-section routing (claim-from-pool + restart recovery)
                    # assertions. resolveWarmPool still falls back to the
                    # "base" pool by name for unannotated routing.
```

依据（已核实）：`refreshWarmPoolRouting` 中 config 覆盖优先；无覆盖时 CRUD 池（带 annotation）接管路由，删除后回落到 templateID 同名 "base" 池——主 lifecycle 段行为不变。

- [ ] **Step 4: YAML 校验 + commit + push（触发 CI 核实 Q1/Q2）**

```bash
python3 -c "import yaml; yaml.safe_load(open('.github/workflows/e2e.yml')); print('yaml OK')"
git add .github/workflows/e2e.yml
git commit -s -m "ci(e2e): align agent-sandbox CRDs, controller and router to v1.0.11 (#86)"
git push -u origin agentsandbox-v1.0.11
```

监视 `E2E (agent-sandbox)`：
- router image pull 失败 → 按 Step 2 注释走 staging digest 回退（解析 digest，独立 commit）
- CRD schema/行为漂移导致用例失败（Q2）→ 按失败日志逐项修（每次独立 commit）

---

### Task 4: agent-sandbox E2E 用例（WARMPOOL_E2E + RESTART_E2E）

**Files:**
- Modify: `hack/kind-e2e/run-examples-e2e.sh`（新增 RESTART_E2E 段）
- Modify: `.github/workflows/e2e.yml`（agent-sandbox job 的 Run E2E tests step 加 exports）

**Interfaces:**
- Consumes: Task 3 的 v1.0.11 环境；现有 `WARMPOOL_E2E` 活体段（warm pool force/graceful + extended lifecycle，opensandbox job 已在用）。
- Produces: `RESTART_E2E=1` 时的重启恢复段；agent-sandbox job 同时获得 warm pool 活体覆盖。

- [ ] **Step 1: runner 新增 RESTART_E2E 段**

在 warm pool 活体段（graceful 段结束的 `fi`）之后插入：

```bash
if [ "${RESTART_E2E:-0}" = "1" ]; then
  echo ""
  echo "=== Gateway restart routing recovery ==="
  SB_NAMESPACE="${SANDBOX_NAMESPACE:-agent-sandbox-system}"
  GW_NS="${GATEWAY_NAMESPACE:-e2bgateway-system}"
  GW_PORT="${GATEWAY_URL##*:}"

  # 1. CRUD-create a pool; its template annotation is what New() restores
  #    routing from after a restart.
  RR_RESP=$(curl -s -w "\n%{http_code}" -X POST "${GATEWAY_URL}/api/v1/warm-pools" \
    -H "X-API-Key: ${E2B_API_KEY}" -H "Content-Type: application/json" \
    -d '{"templateID":"base","targetSize":1,"warmPoolID":"wp-e2e-restart"}')
  [ "$(echo "$RR_RESP" | tail -1)" = "201" ] && pass "Restart: pool created" \
    || fail "Restart: pool create" "HTTP $(echo "$RR_RESP" | tail -1): $(echo "$RR_RESP" | sed '$d')"

  # 2. Wait for one warm sandbox.
  RWARMED=0
  for i in $(seq 1 60); do
    RCUR=$(curl -sf "${GATEWAY_URL}/api/v1/warm-pools/wp-e2e-restart" \
      -H "X-API-Key: ${E2B_API_KEY}" \
      | python3 -c "import sys,json; print(json.load(sys.stdin).get('currentSize',0))" 2>/dev/null)
    if [ "${RCUR:-0}" -ge 1 ]; then RWARMED=1; break; fi
    sleep 3
  done
  [ "$RWARMED" = "1" ] && pass "Restart: pool pre-warmed" \
    || fail "Restart: pool warmup" "currentSize stayed 0 after 180s"

  # 3. Restart the gateway; routing must be rebuilt from CRD annotations.
  kubectl -n "$GW_NS" rollout restart deployment/e2bgateway
  if kubectl -n "$GW_NS" rollout status deployment/e2bgateway --timeout=120s >/dev/null 2>&1; then
    pass "Restart: gateway rollout completed"
  else
    fail "Restart: gateway rollout" "did not become ready in 120s"
  fi

  # The rollout replaced the pod the outer kubectl port-forward was attached
  # to; re-establish the tunnel before any further API calls.
  pkill -f "port-forward.*e2bgateway" 2>/dev/null
  sleep 2
  kubectl port-forward "svc/e2bgateway" -n "$GW_NS" "${GW_PORT}:8080" >/tmp/pf-restart.log 2>&1 &
  for i in $(seq 1 30); do
    curl -sf "${GATEWAY_URL}/healthz" >/dev/null 2>&1 && break
    sleep 2
  done
  curl -sf "${GATEWAY_URL}/healthz" >/dev/null 2>&1 \
    && pass "Restart: gateway healthy after restart" \
    || fail "Restart: gateway health" "not healthy after rollout"

  # 4. Create a sandbox for the pooled template; the restored routing must
  #    send the claim to wp-e2e-restart.
  RS_RESP=$(curl -s -w "\n%{http_code}" -X POST "${GATEWAY_URL}/sandboxes" \
    -H "X-API-Key: ${E2B_API_KEY}" -H "Content-Type: application/json" \
    -d '{"templateID":"base","timeout":300}')
  RS_ID=$(echo "$RS_RESP" | sed '$d' \
    | python3 -c "import sys,json; print(json.load(sys.stdin).get('sandboxID',''))" 2>/dev/null)
  if [ "$(echo "$RS_RESP" | tail -1)" = "201" ] && [ -n "$RS_ID" ]; then
    pass "Restart: sandbox created after restart ($RS_ID)"
  else
    fail "Restart: sandbox create" "HTTP $(echo "$RS_RESP" | tail -1)"
  fi

  sleep 3
  if kubectl get sandboxclaims.extensions.agents.x-k8s.io -n "$SB_NAMESPACE" -o json 2>/dev/null \
     | python3 -c "
import sys, json
claims = json.load(sys.stdin).get('items', [])
hits = [c['metadata']['name'] for c in claims
        if ((c.get('spec') or {}).get('warmPoolRef') or {}).get('name') == 'wp-e2e-restart']
sys.exit(0 if hits else 1)"; then
    pass "Restart: template routing restored to the CRUD-created pool"
  else
    fail "Restart: routing recovery" "no claim references wp-e2e-restart after restart"
  fi

  # 5. Cleanup (best-effort; the gateway lost its in-memory idMap on restart,
  #    so sandbox delete may not resolve and the claim/pool are left to TTL/GC
  #    in the worst case — the kind cluster is ephemeral).
  [ -n "$RS_ID" ] && curl -s -o /dev/null -m 30 -X DELETE "${GATEWAY_URL}/sandboxes/${RS_ID}" -H "X-API-Key: ${E2B_API_KEY}"
  curl -s -o /dev/null -m 60 -X DELETE "${GATEWAY_URL}/api/v1/warm-pools/wp-e2e-restart?force=true" -H "X-API-Key: ${E2B_API_KEY}"
fi
```

注：`warmPoolRef` 的 JSON 路径已核实为 `spec.warmPoolRef.name`（extensions API `WarmPoolRefField = ".spec.warmPoolRef.name"`）。

- [ ] **Step 2: e2e.yml agent-sandbox job 加 exports**

`e2e-agent-sandbox` job 的 `Run E2E tests` step，在 `export SKIP_SDK_TESTS=1` 之后加：

```bash
          export WARMPOOL_E2E=1
          export RESTART_E2E=1
          # Restart section talks to kubectl directly; make targets explicit
          # for the shared runner script.
          export SANDBOX_NAMESPACE=agent-sandbox-system
          export GATEWAY_NAMESPACE=e2bgateway-system
```

注意：agent-sandbox job 的 port-forward 命令行若是 `kubectl port-forward svc/e2bgateway -n e2bgateway-system 8080:8080 &`，与 RESTART 段的 pkill/重建模式匹配（pkill 模式 `port-forward.*e2bgateway` 能命中）。opensandbox job **不加** RESTART_E2E（其池定义是进程内的，重启即失，语义不适用）。

- [ ] **Step 3: 校验 + commit + push + CI 迭代**

```bash
bash -n hack/kind-e2e/run-examples-e2e.sh && echo OK
python3 -c "import yaml; yaml.safe_load(open('.github/workflows/e2e.yml')); print('yaml OK')"
git add hack/kind-e2e/run-examples-e2e.sh .github/workflows/e2e.yml
git commit -s -m "test(e2e): enable warm pool live suite and add gateway restart routing recovery for agent-sandbox (#86)"
git push origin agentsandbox-v1.0.11
```

监视 `E2E (agent-sandbox)`。已知风险与预案：
- warm pool 活体段在 agent-sandbox 上首次跑：force delete 走 Background propagation + label 直删，claim 从池领养——若 claim 等待超时（池预热慢），预热等待已是 180s，失败再看 controller 日志
- extended 段 pause：agent-sandbox 的 pause 走 CRD patch，若 resume 失败由既有 skip 逻辑兜底
- RESTART 段 rollout 后 pf 重建失败 → 检查 /tmp/pf-restart.log 输出并修 pkill 模式

---

### Task 5: 文档（examples README + AGENTS.md 收尾）

**Files:**
- Modify: `examples/README.md`
- Modify: `AGENTS.md`（Task 2 已改 adapter 表行；此处补配置说明与 E2E 版本描述）
- Modify: `configs/e2bgateway-example.yaml`（仅当存在 `useEnvdDataPlane` 示例行时删除——先 grep 确认，现无则跳过）

**Interfaces:**
- Consumes: Task 2/3 的最终状态。

- [ ] **Step 1: README 增加 backend 配置段**

在 `## Running Against E2BGateway` 段之后追加（格式随文件现有 markdown 风格）：

````markdown
## Backend Configuration (agent-sandbox)

The examples work against any backend. For the agent-sandbox backend, the
gateway config looks like:

```yaml
backends:
  - name: agent-sandbox
    type: agent-sandbox
    enabled: true
    config:
      namespace: agent-sandbox-system
      # Default claim pool for templates without an explicit mapping.
      warmPoolName: base
      # Optional: E2B template ID -> SandboxTemplate resource name
      # (default: the template ID is used as the SandboxTemplate name).
      templateToSandboxTemplate:
        my-e2b-template: my-sandbox-template
      # Optional: operator-pinned template -> warm pool routing overrides.
      # These always win over pools created via the warm-pool CRUD API.
      templateToWarmPool:
        my-e2b-template: my-pool
      # OnReplenish (CRD default) or Recreate; applied to CRUD-created pools.
      warmPoolUpdateStrategy: OnReplenish
      maxWarmPoolSize: 256
```

Sandbox pods must embed the envd daemon (the gateway's only data plane);
build the reference image with
`docker build -t sandbox-with-envd:local images/sandbox/`.
````

- [ ] **Step 2: AGENTS.md 收尾**

- 确认 Task 2 已改 adapter 表行；再全文 `grep -n "useEnvdDataPlane\|ViaHandle\|SDK handle\|legacy" AGENTS.md`，把数据面 dual-path 描述清干净
- E2E 相关行（如有提及 agent-sandbox 版本）更新为 v1.0.11；无则不动

- [ ] **Step 3: 校验 + commit**

```bash
grep -rn "useEnvdDataPlane" configs/ AGENTS.md examples/README.md   # expect: no output
git add examples/README.md AGENTS.md configs/ 2>/dev/null
git commit -s -m "docs: agent-sandbox backend config example; envd-only data plane docs (#86)"
```

---

### Task 6: 收尾验证 + PR

- [ ] **Step 1: 全量门槛**

Run: `make pre-commit`
Expected: exit 0。

- [ ] **Step 2: 对照 issue #86 验收清单**

- go.mod = v1.0.11 ✅（Task 1）
- E2E CRD/controller/image/router 全对齐 v1.0.11、无 latest-* ✅（Task 3；router 若走 digest 回退需在 PR 注明）
- legacy handle 路径移除 ✅（Task 2）
- agent-sandbox E2E warm pool force + 路由恢复用例 ✅（Task 4）
- examples README backend 配置段 ✅（Task 5）

- [ ] **Step 3: push + PR**

```bash
git push origin agentsandbox-v1.0.11
```

按 submit-pr skill 开 PR：
- 标题：`refactor(agentsandbox)!: align with v1.0.11 — remove legacy data plane, pin E2E, live warm pool coverage (#86)`
- Body：Summary、**BREAKING CHANGE 段**（useEnvdDataPlane 移除 + 迁移指引）、Changes、Testing、Documentation、Checklist；`Closes #86`
- 若 SDK 摩擦点在上游已有对应修复（v1.0.11 release notes 对照），在 body 注明无需另开 upstream issue

- [ ] **Step 4: 监视 CI + CodeRabbit**

后台 watch `gh pr checks`；评论按 receiving-code-review 流程逐条核实后修复/回复/resolve。

---

## Checklist

- [ ] Spec reviewed（`2026-11-14-agentsandbox-v1.0.11-design.md`；Q1/Q2 在 Task 3 CI 首跑核实，Q3 已核实：`spec.warmPoolRef.name`）
- [ ] `go.mod` = v1.0.11 且 `TestWarmPoolNameHash` 通过（hash 方案未变）
- [ ] `useEnvdDataPlane`/`ViaHandle`/`getHandle` 全仓零残留（docs/superpowers 历史文档除外）
- [ ] E2E 无 `latest-*` 移动目标
- [ ] agent-sandbox job：WARMPOOL_E2E + RESTART_E2E 活体通过
- [ ] opensandbox job 回归绿（不受影响验证）
- [ ] BREAKING CHANGE 在 commit footer、PR 描述、AGENTS.md 三处标注
- [ ] `make pre-commit` 全绿；所有 commit 带 Signed-off-by
