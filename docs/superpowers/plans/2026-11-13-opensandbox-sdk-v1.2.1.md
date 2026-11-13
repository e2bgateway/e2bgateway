# OpenSandbox SDK v1.2.1 + 真实 server E2E + examples 修复 实现计划（#85 Phase 1）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** SDK 升到 v1.2.1；opensandbox E2E 从 Python mock controller 切换为真实 OpenSandbox server release-1.2.1 并新增 warm pool force delete 等活体用例；修复 examples/python/warm_pools.py 与 curl 文档；移除死文件。

**Architecture:** 真实 server 以容器跑在 GitHub runner 宿主上（挂 docker.sock，沙箱为宿主兄弟容器，无 DinD）；kind 内 gateway 经宿主 docker bridge IP `172.17.0.1:8090` 访问。E2E 用例走 `hack/kind-e2e/run-examples-e2e.sh`（curl/bash，活体后端）；`test/e2e/*.go` 是 mock-backend 形状测试，不动。

**Tech Stack:** Go 1.26、OpenSandbox Go SDK v1.2.1、`opensandbox/server:release-1.2.1` 容器、kind、GitHub Actions。

**Spec:** `docs/superpowers/specs/2026-11-13-opensandbox-sdk-v1.2.1-design.md`

## Global Constraints

- CI 门槛：`make pre-commit` 全绿；commit 一律 `git commit -s`（DCO）。
- **镜像 tag（Q1 已解决）**：release BOM `1.2.1.yaml` 确认全套组件统一 tag **`release-1.2.1`**——`opensandbox/server:release-1.2.1`（digest `sha256:07f9b39a…`，与本地已下载镜像一致）、`opensandbox/execd:release-1.2.1`、`opensandbox/egress:release-1.2.1`。CI runner 拉不动 Docker Hub 时备选 `sandbox-registry.cn-zhangjiakou.cr.aliyuncs.com/opensandbox/*`。
- **本地验证可用**：本机已有 server 镜像 + docker daemon（29.2.1），Task 3/4 均含本地冒烟步骤；execd 等镜像本机若拉取失败（Docker Hub 不稳），涉及真实沙箱创建的断言顺延到 CI 验证。Q3（kind 内 172.17.0.1 可达性）仍只能在 CI 核实。
- `test/e2e/*.go`（mock backend 形状测试）与 agent-sandbox E2E job **不动**。
- examples 修复只做 python + curl 文档；go/js warm pool 示例是 Phase 2（#85 后续 branch）。
- 每 task 一个 commit，conventional commits + `-s`。

---

### Task 1: SDK bump v1.2.0 → v1.2.1 + 回归

**Files:**
- Modify: `go.mod`、`go.sum`

**Interfaces:**
- Produces: 全仓编译与测试基于 SDK v1.2.1；后续 task 依赖此基线。

- [ ] **Step 1: 升级**

```bash
go get github.com/alibaba/OpenSandbox/sdks/sandbox/go@v1.2.1
go mod tidy
```

- [ ] **Step 2: 编译 + 全量回归**

Run: `go build ./... && go vet ./...`
Expected: 无错误（v1.2.1 无导出 API 签名变更；若编译报错，逐项评估差异，不得为编译通过而回退 gateway 行为）。

Run: `go test ./... -race`
Expected: 全部 PASS。**重点核对** opensandbox 包 drain/shutdown 相关（`TestDeleteWarmPool_*`，上游 #2616/#2617 改了 pool acquire/drain 取消语义）与 streaming 相关测试。

- [ ] **Step 3: pre-commit + commit**

```bash
make pre-commit   # exit 0
git add go.mod go.sum
git commit -s -m "chore(deps): bump OpenSandbox Go SDK to v1.2.1"
```

---

### Task 2: examples 修复（warm_pools.py + curl 文档）

**Files:**
- Modify: `examples/python/warm_pools.py`（整文件重写）
- Modify: `examples/curl/api_examples.sh`（markdown 文档，加 Warm Pools 段）

**Interfaces:**
- Produces: `warm_pools.py` 失败时**非零退出**（Task 4 的 runner 依赖退出码判 pass/fail）；使用 root 路径 `{base}/warm-pools`（与其他示例一致，gateway 同时注册了 root 与 `/api/v1` 两组路由）。

- [ ] **Step 1: 重写 warm_pools.py**

现状 bug（issue #85 §4）：请求体用 `size`（应为 `targetSize`）、响应解析 `warmPoolId`/`id`（应为 `warmPoolID`）→ `pool_id` 恒空 → 步骤 3-5 静默跳过；且从不检查 HTTP 状态码。整文件替换为：

```python
"""Warm Pools - Manage pre-warmed sandbox pools.

Demonstrates warm pool operations:
1. List warm pools
2. Create a warm pool
3. Get warm pool details
4. Update pool size
5. Delete warm pool (graceful)
6. Force delete (?force=true — immediate teardown)

Usage:
    export E2B_DOMAIN=localhost:8080
    export E2B_API_KEY=your-api-key
    python warm_pools.py
"""

import os
import sys

import requests


def main():
    base_url = os.environ.get("E2B_DOMAIN", "localhost:8080")
    if not base_url.startswith("http"):
        base_url = f"http://{base_url}"
    api_key = os.environ.get("E2B_API_KEY", "test-key")
    headers = {"X-API-Key": api_key, "Content-Type": "application/json"}

    # 1. List warm pools
    print("1. Listing warm pools...")
    resp = requests.get(f"{base_url}/warm-pools", headers=headers, timeout=30)
    resp.raise_for_status()
    print(f"   Existing pools: {len(resp.json())}")

    # 2. Create a warm pool
    print("2. Creating warm pool...")
    resp = requests.post(f"{base_url}/warm-pools", headers=headers, timeout=30, json={
        "templateID": "base",
        "targetSize": 3,
    })
    resp.raise_for_status()
    pool = resp.json()
    pool_id = pool["warmPoolID"]
    print(f"   Pool: {pool_id} (template={pool.get('templateID')}, target={pool.get('targetSize')})")

    # 3. Get warm pool details
    print("3. Getting warm pool details...")
    resp = requests.get(f"{base_url}/warm-pools/{pool_id}", headers=headers, timeout=30)
    resp.raise_for_status()
    detail = resp.json()
    print(f"   Status: {detail.get('status')}, "
          f"target={detail.get('targetSize')}, current={detail.get('currentSize')}")

    # 4. Update pool size
    print("4. Updating pool size...")
    resp = requests.post(f"{base_url}/warm-pools/{pool_id}/size", headers=headers,
                         timeout=30, json={"targetSize": 5})
    resp.raise_for_status()
    print(f"   Updated: HTTP {resp.status_code}")

    # 5. Delete warm pool (graceful: drain in-flight work, then release idle)
    print("5. Deleting warm pool (graceful)...")
    resp = requests.delete(f"{base_url}/warm-pools/{pool_id}", headers=headers, timeout=120)
    resp.raise_for_status()
    print(f"   Deleted: HTTP {resp.status_code}")

    # 6. Force delete: create another pool and tear it down immediately.
    #    force=true skips the graceful wait (opensandbox: non-graceful
    #    shutdown + idle drain; agent-sandbox: cascade-delete unclaimed
    #    sandboxes). Claimed sandboxes are never affected.
    print("6. Force-deleting a warm pool...")
    resp = requests.post(f"{base_url}/warm-pools", headers=headers, timeout=30, json={
        "templateID": "base",
        "targetSize": 1,
    })
    resp.raise_for_status()
    force_id = resp.json()["warmPoolID"]
    resp = requests.delete(f"{base_url}/warm-pools/{force_id}",
                           headers=headers, params={"force": "true"}, timeout=120)
    resp.raise_for_status()
    print(f"   Force deleted {force_id}: HTTP {resp.status_code}")

    print("\nDone.")


if __name__ == "__main__":
    try:
        main()
    except requests.RequestException as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        sys.exit(1)
```

- [ ] **Step 2: 语法校验**

Run: `python3 -m py_compile examples/python/warm_pools.py && echo OK`
Expected: OK。（活体验证在 Task 4 的 CI E2E 中：runner 会执行此文件并检查退出码。）

- [ ] **Step 3: api_examples.sh 加 Warm Pools 段**

该文件是 **markdown 文档**（runner 脚本 231 行注释明确 "not an executable script"），沿用现有 `## 段 + ```bash 围栏` 风格，在 tags 示例段（`### Delete tag`，约 line 195）之后插入：

````markdown
## Warm Pools

### List warm pools
```bash
curl "$GATEWAY_URL/warm-pools" -H "X-API-Key: $E2B_API_KEY"
```

### Create warm pool
```bash
curl -X POST "$GATEWAY_URL/warm-pools" \
  -H "X-API-Key: $E2B_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"templateID": "base", "targetSize": 3}'
```

Expected response (201):
```json
{
  "warmPoolID": "wp-base-abcd1234",
  "templateID": "base",
  "targetSize": 3,
  "currentSize": 0,
  "status": "scaling",
  "createdAt": "2026-11-13T08:00:00Z"
}
```

### Get warm pool
```bash
curl "$GATEWAY_URL/warm-pools/wp-base-abcd1234" -H "X-API-Key: $E2B_API_KEY"
```

### Resize warm pool
```bash
curl -X POST "$GATEWAY_URL/warm-pools/wp-base-abcd1234/size" \
  -H "X-API-Key: $E2B_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"targetSize": 5}'
```

### Delete warm pool (graceful)
```bash
curl -X DELETE "$GATEWAY_URL/warm-pools/wp-base-abcd1234" \
  -H "X-API-Key: $E2B_API_KEY"
```

### Delete warm pool (force)
Immediate teardown — opensandbox: non-graceful shutdown + idle drain;
agent-sandbox: cascade-delete unclaimed sandboxes. Claimed sandboxes are
never affected. Invalid `force` values return 400.
```bash
curl -X DELETE "$GATEWAY_URL/warm-pools/wp-base-abcd1234?force=true" \
  -H "X-API-Key: $E2B_API_KEY"
```
````

- [ ] **Step 4: Commit**

```bash
git add examples/python/warm_pools.py examples/curl/api_examples.sh
git commit -s -m "fix(examples): correct warm pool API fields and add force delete demo"
```

---

### Task 3: E2E 切换真实 OpenSandbox server v1.2.1（K8s / Helm umbrella chart）

> 注：本 branch 已有一个宿主 docker 方案的提交（`ci(e2e): run opensandbox E2E against real server release-1.2.1 (retire mock controller)`），本 task 以新提交将其改造为 K8s 部署（用户决策：真实 server 也部署在 K8s 上）。

**Files:**
- Create: `test/kind-e2e/manifests/opensandbox/values-e2e.yaml`
- Delete: `test/kind-e2e/manifests/opensandbox/server-config.toml`（docker-runtime 时代产物；K8s 模式下 server 配置由 chart `configToml` 管理）
- Modify: `.github/workflows/e2e.yml`（`e2e-opensandbox` job）

**Interfaces:**
- Produces: 集群内 `http://opensandbox-server.opensandbox-system/v1`（Service 名由 chart `fullnameOverride: opensandbox-server` 固定，端口 80）；沙箱工作负载 namespace `opensandbox`；Task 4 用例在此后端上跑。

**已核实事实**（2026-11-13，来自上游 `manifests/HELM-DEPLOYMENT.md` 与 chart values）：
- umbrella chart `manifests/charts/opensandbox` = base(CRDs+RBAC) + controller + server（ingress-gateway/node-agent/fast-sandbox 默认关）；chart 不发布 tgz，需 clone `release-1.2.1` tag 安装
- server chart 默认 `configToml`：`port = 80`、`api_key = ""`、`[runtime] type = "kubernetes"`、execd_image 已 pin `release-1.2.1`（aliyun registry，GH runner 可达）、`[kubernetes] namespace = "opensandbox"`（chart 不创建该 ns，需预建）、workload_provider = batchsandbox
- chart deployment **不**自带 `OPENSANDBOX_INSECURE_SERVER` env，而空 api_key 需要它跳过 startup guard → 必须由 values 注入
- ingress-gateway 官方标注 "REQUIRED on Kubernetes deployments" 是针对**集群外**客户端；e2bgateway 与沙箱 Pod 同集群，direct 模式返回的 Pod IP 在 kind 扁平网络可直达 → 先不启用，CI 首跑不通再启用并配 `opensandbox-server.server.gateway.*`

- [ ] **Step 1: 创建 values-e2e.yaml**

```yaml
# E2E overrides for the OpenSandbox umbrella chart (release-1.2.1).
#
# CI ONLY: the lifecycle server runs without an API key
# (OPENSANDBOX_INSECURE_SERVER acknowledges the startup guard). Never reuse
# these values for a real deployment.
#
# Everything else comes from the chart defaults pinned to release-1.2.1:
# runtime type=kubernetes, execd image, sandbox workload namespace
# "opensandbox" (created by the e2e workflow before install), port 80.
opensandbox-server:
  server:
    env:
      - name: OPENSANDBOX_INSECURE_SERVER
        value: "YES"
```

- [ ] **Step 2: 删除 server-config.toml**

```bash
git rm test/kind-e2e/manifests/opensandbox/server-config.toml
```

- [ ] **Step 3: e2e.yml — 用 helm 部署替换宿主 docker 步骤**

将 `- name: Start OpenSandbox server (release-1.2.1)` 整个 step 替换为：

```yaml
      - name: Deploy OpenSandbox server (release-1.2.1, helm)
        run: |
          set -e
          # Charts are versioned sources in the upstream repo (not published
          # as tgz); clone the exact release tag and install the umbrella chart
          # (CRDs + controller + lifecycle server).
          git clone --depth 1 --branch release-1.2.1 https://github.com/alibaba/OpenSandbox.git /tmp/opensandbox
          cd /tmp/opensandbox/manifests/charts
          helm dependency build opensandbox

          # Sandbox workload namespace — the chart does not create it.
          kubectl create namespace opensandbox --dry-run=client -o yaml | kubectl apply -f -

          helm install opensandbox opensandbox \
            --namespace opensandbox-system --create-namespace \
            -f "$GITHUB_WORKSPACE/test/kind-e2e/manifests/opensandbox/values-e2e.yaml" \
            --wait --timeout 5m
          kubectl -n opensandbox-system get pods

          # Probe the lifecycle API from the runner via a temporary port-forward.
          kubectl -n opensandbox-system port-forward svc/opensandbox-server 18090:80 >/tmp/pf.log 2>&1 &
          PF_PID=$!
          for i in $(seq 1 60); do
            if curl -sf http://localhost:18090/v1/sandboxes >/dev/null 2>&1; then
              echo "OpenSandbox server is ready"
              break
            fi
            echo "Waiting for opensandbox server... ($i)"
            sleep 2
          done
          curl -sf http://localhost:18090/v1/sandboxes >/dev/null || {
            echo "OpenSandbox server failed to come up:"
            kubectl -n opensandbox-system logs deploy/opensandbox-server --tail=100 || true
            kill $PF_PID 2>/dev/null || true
            exit 1
          }
          kill $PF_PID 2>/dev/null || true
```

- [ ] **Step 4: e2e.yml — gateway ConfigMap 指向集群内 Service**

```yaml
                    baseURL: "http://172.17.0.1:8090/v1"
```

改为：

```yaml
                    baseURL: "http://opensandbox-server.opensandbox-system/v1"
```

`templateToImage."base": "sandbox-with-envd:local"` 保留（镜像已 `kind load`，沙箱 Pod 在 kind 节点本地可解析；若 controller 的 Pod 模板用 `imagePullPolicy: Always` 导致拉取失败，回退方案见 Step 7 迭代预案）。

- [ ] **Step 5: e2e.yml — 诊断与清理**

`Collect diagnostics` 中的 docker logs 块替换为：

```yaml
          echo "=== OpenSandbox server logs ==="
          kubectl logs -n opensandbox-system deploy/opensandbox-server --tail=100 || true
          echo "=== OpenSandbox controller logs ==="
          kubectl logs -n opensandbox-system deploy/opensandbox-controller --tail=100 || true
          echo "=== Sandbox workload pods ==="
          kubectl get pods -n opensandbox -o wide || true
          kubectl describe pods -n opensandbox 2>/dev/null | grep -A5 "Events:" | head -40 || true
```

`Cleanup kind cluster` step 中删除 `docker rm -f opensandbox-server || true`（kind delete 已覆盖）。

- [ ] **Step 6: 本地冒烟（kind + helm 全流程）**

```bash
kind create cluster --name osbx-smoke
kubectl --context kind-osbx-smoke create namespace opensandbox
git clone --depth 1 --branch release-1.2.1 https://github.com/alibaba/OpenSandbox.git /tmp/opensandbox
cd /tmp/opensandbox/manifests/charts && helm dependency build opensandbox
helm install opensandbox opensandbox --namespace opensandbox-system --create-namespace \
  -f "$OLDPWD/test/kind-e2e/manifests/opensandbox/values-e2e.yaml" --wait --timeout 5m
kubectl --context kind-osbx-smoke -n opensandbox-system get pods
kubectl --context kind-osbx-smoke -n opensandbox-system port-forward svc/opensandbox-server 18090:80 &
sleep 3; curl -sf http://localhost:18090/v1/sandboxes && echo READY
# 冒烟完成后：
kill %1; kind delete cluster --name osbx-smoke
```

Expected: 三个组件 Pod Ready，`GET /v1/sandboxes` 返回 200 JSON。失败则按 server/controller 日志修 values 后重试（阻断项）。

- [ ] **Step 7: YAML 校验 + commit + push（CI 首跑核实 Q3 替代未知项）**

```bash
python3 -c "import yaml; yaml.safe_load(open('.github/workflows/e2e.yml')); print('yaml OK')"
git add -A
git commit -s -m "ci(e2e): deploy opensandbox server release-1.2.1 in-cluster via helm umbrella chart"
git push origin opensandbox-sdk-v1.2.1
```

CI 迭代预案（每次修复独立 `-s` commit）：
- 沙箱 Pod `ImagePullBackOff`（`sandbox-with-envd:local` 被强制拉取）→ 在 kind 旁起本地 registry（kind 文档标准模式），push 后 `templateToImage` 改 `localhost:5000/sandbox-with-envd:local`
- 数据面（envd proxy）不通 → 启用 ingress-gateway 子 chart + `opensandbox-server.server.gateway.*` 公告配置
- BatchSandbox 资源不足 → values 里下调 server/controller resources 或 kind 节点资源说明

---

### Task 4: E2E 活体用例（warm pool + force delete + 扩展生命周期）

**Files:**
- Modify: `hack/kind-e2e/run-examples-e2e.sh`（两个 job 共享，新段落用 env gate）
- Modify: `.github/workflows/e2e.yml`（opensandbox job 的 "Run E2E tests" step 加一个 env）

**Interfaces:**
- Consumes: Task 3 的集群内真实 server；Task 2 的 `warm_pools.py`（非零退出 = 失败）。
- Produces: `WARMPOOL_E2E=1` 时 runner 执行活体段落（create → 等预热 → 从池创建沙箱 → force delete → 断言 claimed 沙箱存活、池 404）+ 扩展生命周期段（renew/ports/pause/resume）。

- [ ] **Step 1: runner 加 Python HTTP-only 示例段（不受 SKIP_SDK_TESTS 门控）**

在 `=== Python Examples ===` 段之后插入（`warm_pools.py` 只用 `requests` 打 gateway REST API，不依赖 ConnectRPC 数据面）：

```bash
echo ""
echo "=== Python HTTP-only Examples ==="
cd "$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
for ex in warm_pools.py; do
  echo "--- Python(HTTP): $ex ---"
  if E2B_DOMAIN="${E2B_DOMAIN}" E2B_API_KEY="${E2B_API_KEY}" \
     timeout 300 python3 ./examples/python/${ex} 2>&1 | tee /tmp/py-${ex}.log | tail -8; then
    if [ "${PIPESTATUS[0]}" = "0" ]; then
      pass "Python(HTTP): $ex"
    else
      fail "Python(HTTP): $ex" "exit code non-zero (see /tmp/py-${ex}.log)"
    fi
  else
    fail "Python(HTTP): $ex" "timeout"
  fi
done
```

注：agent-sandbox job 也会跑此段——warm pool CRUD + force 在 agentsandbox adapter 同样受支持（#84），其 E2E 集群已含所需 CRD 与 `base` 模板。若 CI 首跑 agent-sandbox job 因此段失败，将 for 循环用 `WARMPOOL_E2E` 门控并只在 opensandbox job 置 1。

- [ ] **Step 2: runner 加活体 warm pool force-delete 段**

```bash
if [ "${WARMPOOL_E2E:-0}" = "1" ]; then
  echo ""
  echo "=== Warm Pool live force-delete ==="

  # 1. Create a pool with an explicit ID.
  WP_RESP=$(curl -s -w "\n%{http_code}" -X POST "${GATEWAY_URL}/api/v1/warm-pools" \
    -H "X-API-Key: ${E2B_API_KEY}" -H "Content-Type: application/json" \
    -d '{"templateID":"base","targetSize":2,"warmPoolID":"wp-e2e-force"}')
  WP_CODE=$(echo "$WP_RESP" | tail -1)
  if [ "$WP_CODE" = "201" ]; then
    pass "Warm pool create"
  else
    fail "Warm pool create" "HTTP $WP_CODE: $(echo "$WP_RESP" | sed '$d')"
  fi

  # 2. Wait for at least one idle sandbox to be pre-warmed (real backend).
  WARMED=0
  for i in $(seq 1 60); do
    CUR=$(curl -sf "${GATEWAY_URL}/api/v1/warm-pools/wp-e2e-force" \
      -H "X-API-Key: ${E2B_API_KEY}" \
      | python3 -c "import sys,json; print(json.load(sys.stdin).get('currentSize',0))" 2>/dev/null)
    if [ "${CUR:-0}" -ge 1 ]; then WARMED=1; break; fi
    sleep 3
  done
  [ "$WARMED" = "1" ] && pass "Warm pool pre-warms idle sandboxes" \
    || fail "Warm pool warmup" "currentSize stayed 0 after 180s"

  # 3. Create a sandbox for the same template — served from the pool (claimed).
  CLAIM_RESP=$(curl -s -w "\n%{http_code}" -X POST "${GATEWAY_URL}/sandboxes" \
    -H "X-API-Key: ${E2B_API_KEY}" -H "Content-Type: application/json" \
    -d '{"templateID":"base","timeout":300}')
  CLAIM_CODE=$(echo "$CLAIM_RESP" | tail -1)
  CLAIM_ID=$(echo "$CLAIM_RESP" | sed '$d' \
    | python3 -c "import sys,json; print(json.load(sys.stdin).get('sandboxID',''))" 2>/dev/null)
  if [ "$CLAIM_CODE" = "201" ] && [ -n "$CLAIM_ID" ]; then
    pass "Create sandbox from warm pool ($CLAIM_ID)"
  else
    fail "Create sandbox from warm pool" "HTTP $CLAIM_CODE"
  fi

  # 4. Force-delete the pool.
  DEL_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE \
    "${GATEWAY_URL}/api/v1/warm-pools/wp-e2e-force?force=true" \
    -H "X-API-Key: ${E2B_API_KEY}")
  [ "$DEL_CODE" = "204" ] && pass "Force delete warm pool" \
    || fail "Force delete warm pool" "HTTP $DEL_CODE"

  # 5. Pool is gone.
  GET_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
    "${GATEWAY_URL}/api/v1/warm-pools/wp-e2e-force" -H "X-API-Key: ${E2B_API_KEY}")
  [ "$GET_CODE" = "404" ] && pass "Force-deleted pool returns 404" \
    || fail "Force-deleted pool returns 404" "HTTP $GET_CODE"

  # 6. The claimed sandbox survives the force delete and stays usable.
  if [ -n "$CLAIM_ID" ]; then
    sleep 5
    CMD_RESP=$(curl -s --max-time 30 -X POST "${GATEWAY_URL}/sandboxes/${CLAIM_ID}/commands" \
      -H "X-API-Key: ${E2B_API_KEY}" -H "Content-Type: application/json" \
      -d '{"command":"echo alive-after-force-delete"}')
    echo "$CMD_RESP" | grep -q "alive-after-force-delete" \
      && pass "Claimed sandbox survives force delete" \
      || fail "Claimed sandbox survives force delete" "$CMD_RESP"
  fi
fi
```

注：断言 6 依赖 envd 数据面（commands 经 gateway envd proxy → 沙箱）。opensandbox job 现 `SKIP_SDK_TESTS=1` 是因 **mock** 无数据面；真实 server 下应可用。若 CI 首跑因数据面失败，降级为 `GET /sandboxes/{id}` 返回 200 的存活检查，数据面问题另开跟进项。

- [ ] **Step 3: runner 加扩展生命周期用例段（同一 gate；pause/resume 为尽力而为）**

路由已核实：`POST /sandboxes/{id}/timeout|pause|resume`、`GET /sandboxes/{id}/ports`；timeout body `{"timeout": <秒>}`。WS streaming 不做活体（bash 无法驱动 WebSocket，由 mock go 测试覆盖）。**pause/resume 在 K8s 模式依赖 snapshot registry（Q4），未配置时记 skip 不记 fail**：

```bash
if [ "${WARMPOOL_E2E:-0}" = "1" ]; then
  echo ""
  echo "=== Extended lifecycle (live backend) ==="

  EXT_RESP=$(curl -s -w "\n%{http_code}" -X POST "${GATEWAY_URL}/sandboxes" \
    -H "X-API-Key: ${E2B_API_KEY}" -H "Content-Type: application/json" \
    -d '{"templateID":"base","timeout":120}')
  EXT_ID=$(echo "$EXT_RESP" | sed '$d' \
    | python3 -c "import sys,json; print(json.load(sys.stdin).get('sandboxID',''))" 2>/dev/null)
  if [ -n "$EXT_ID" ]; then
    pass "Extended: sandbox created ($EXT_ID)"
  else
    fail "Extended: sandbox create" "$(echo "$EXT_RESP" | sed '$d')"
  fi

  if [ -n "$EXT_ID" ]; then
    # renew timeout
    T_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
      "${GATEWAY_URL}/sandboxes/${EXT_ID}/timeout" \
      -H "X-API-Key: ${E2B_API_KEY}" -H "Content-Type: application/json" \
      -d '{"timeout":300}')
    { [ "$T_CODE" = "204" ] || [ "$T_CODE" = "200" ]; } \
      && pass "Extended: renew timeout" || fail "Extended: renew timeout" "HTTP $T_CODE"

    # ports list (empty is fine — endpoint must answer 200 + JSON)
    P_CODE=$(curl -s -o /tmp/ports.json -w "%{http_code}" \
      "${GATEWAY_URL}/sandboxes/${EXT_ID}/ports" -H "X-API-Key: ${E2B_API_KEY}")
    if [ "$P_CODE" = "200" ] && python3 -c "import json; json.load(open('/tmp/ports.json'))" 2>/dev/null; then
      pass "Extended: list ports"
    else
      fail "Extended: list ports" "HTTP $P_CODE"
    fi

    # pause → resume: best-effort. K8s runtime pause needs snapshot infra
    # (image-committer + a pushable registry) which CI does not configure
    # yet (issue #85 Q4) — skip instead of fail when the backend refuses.
    PAUSE_CODE=$(curl -s -o /tmp/pause.out -w "%{http_code}" -X POST \
      "${GATEWAY_URL}/sandboxes/${EXT_ID}/pause" -H "X-API-Key: ${E2B_API_KEY}")
    if [ "$PAUSE_CODE" = "204" ] || [ "$PAUSE_CODE" = "200" ]; then
      pass "Extended: pause"
      sleep 3
      RESUME_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
        "${GATEWAY_URL}/sandboxes/${EXT_ID}/resume" -H "X-API-Key: ${E2B_API_KEY}")
      { [ "$RESUME_CODE" = "200" ] || [ "$RESUME_CODE" = "204" ]; } \
        && pass "Extended: resume" || fail "Extended: resume" "HTTP $RESUME_CODE"
    else
      skip "Extended: pause/resume" "HTTP $PAUSE_CODE (snapshot registry not configured in CI)"
    fi

    # cleanup
    curl -s -o /dev/null -X DELETE "${GATEWAY_URL}/sandboxes/${EXT_ID}" \
      -H "X-API-Key: ${E2B_API_KEY}"
  fi
fi
```

- [ ] **Step 4: e2e.yml opensandbox job 设置 gate**

`Run E2E tests` step 的 export 列表（`export SKIP_SDK_TESTS=1` 附近）加：

```bash
          export WARMPOOL_E2E=1
```

（agent-sandbox job 不加。）

- [ ] **Step 5: 本地静态校验 + 尽力本地活体验证 + commit + push**

```bash
bash -n hack/kind-e2e/run-examples-e2e.sh && echo "syntax OK"
```

本地活体验证（依赖 Task 3 Step 6 的本地 kind 集群；沙箱镜像 `sandbox-with-envd:local` 需 `docker build -f images/sandbox/Dockerfile images/sandbox/` + `kind load`；镜像构建受本机网络影响，失败则只验 server/gateway 启动 + CRUD，预热断言留给 CI）：

```bash
# 1) kind 集群 + helm 安装 OpenSandbox（同 Task 3 Step 6）
# 2) port-forward server: kubectl -n opensandbox-system port-forward svc/opensandbox-server 18090:80 &
# 3) 起本地 gateway：
cat > /tmp/e2bgw-local.yaml <<'YAML'
server:
  http:
    address: "127.0.0.1:18080"
auth:
  providers:
    - type: apikey
      keys: ["e2b_local_test_key"]
backends:
  - name: opensandbox
    type: opensandbox
    enabled: true
    config:
      baseURL: "http://localhost:18090/v1"
      templateToImage:
        "base": "sandbox-with-envd:local"
routing:
  defaultBackend: opensandbox
  strategy: static
YAML
go run ./cmd/e2bgateway --config /tmp/e2bgw-local.yaml &
# 4) 跑修复后的示例与 runner 活体段：
E2B_DOMAIN=127.0.0.1:18080 E2B_API_KEY=e2b_local_test_key \
  python3 examples/python/warm_pools.py
GATEWAY_URL=http://127.0.0.1:18080 E2B_API_KEY=e2b_local_test_key \
  WARMPOOL_E2E=1 bash hack/kind-e2e/run-examples-e2e.sh
```

Expected: `warm_pools.py` 6 步全过且退出码 0；runner 活体段 pass（受本地镜像可用性限制，失败若纯属镜像/资源问题不阻塞提交，CI 复验）。

```bash
git add hack/kind-e2e/run-examples-e2e.sh .github/workflows/e2e.yml
git commit -s -m "test(e2e): add live warm pool force-delete coverage against real server (#85)"
git push origin opensandbox-sdk-v1.2.1
```

- [ ] **Step 6: 监视 CI 并迭代**

`gh pr checks` / `gh run watch` 盯 `E2E (opensandbox)` 与 `E2E (agent-sandbox)`；失败按 Task 3 Step 7 与 Step 1/2 注记的预案迭代，每次修复独立 commit（`-s`）。

---

### Task 5: 收尾验证 + PR

**Files:**
- 无新改动（验证 + PR）

- [ ] **Step 1: 全量门槛**

Run: `make pre-commit`
Expected: exit 0。

- [ ] **Step 2: 对照 issue #85 验收清单（Phase 1 范围）**

- SDK v1.2.1 ✅（Task 1）；E2E 真实 server v1.2.1 且无 mock ✅（Task 3）；force delete E2E 用例 ✅（Task 4）；`server-config.toml` 死文件消除（重写为实际使用）✅（Task 3）；`warm_pools.py` 修复 + force 演示 ✅（Task 2）。
- Phase 2 项（PoolManager/EndpointCache 评估、go/js 示例）**不在本 PR**，在 PR 描述中注明并链接 #85。

- [ ] **Step 3: 按 submit-pr skill 开 PR**

- 标题：`chore(opensandbox): adopt SDK v1.2.1 and run E2E against the real server (#85)`
- Body：Summary（Phase 1 范围 + mock 退役说明）、Changes、Testing（单测回归重点 + E2E 新用例）、Documentation（examples 修复）、Checklist；注明 **Part of #85**（Phase 2 另开 PR，不用 `Closes`）。
- PR 开出后逐条关注 CodeRabbit 评论。

---

## Checklist

- [ ] Spec reviewed（`2026-11-13-opensandbox-sdk-v1.2.1-design.md`，Q1-Q3 在 Task 3 CI 首跑核实）
- [ ] `go.mod` = SDK v1.2.1，`make pre-commit` 全绿
- [ ] mock controller（deployment.yaml）删除，无残留引用
- [ ] E2E 无 `latest-*` 移动目标（server/execd 镜像 pin 版本）
- [ ] warm pool force delete 活体用例通过（claimed 存活 + 池 404）
- [ ] `warm_pools.py` 在 CI E2E 中执行并通过
- [ ] 所有 commit 带 `Signed-off-by`（DCO）
- [ ] PR 描述标注 Part of #85 + Phase 2 范围说明
