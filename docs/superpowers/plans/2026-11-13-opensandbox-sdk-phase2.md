# OpenSandbox SDK 采纳 Phase 2 实现计划（#85）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** force delete 采纳 SDK `SandboxPoolManager.Destroy`（fence+tombstone+计数）；lifecycle client 启用 `WithRetry`；整体删除冗余的 `endpointHeaders` 手工缓存；新增 go/js warm pool 示例并纳入 CI 活体验证。

**Architecture:** 全部变更限于 opensandbox adapter + examples + runner 脚本。pool 与 manager 共享同一显式 state store（memory 用 `NewInMemoryPoolStateStore()`，redis 不变）；force teardown = `Shutdown(false)` + `Destroy(Force, TombstoneTTL=5m)`；graceful 路径零变化。

**Tech Stack:** Go 1.26、OpenSandbox Go SDK v1.2.1（已在 main）、Node ≥18（runner 已有）、testify。

**Spec:** `docs/superpowers/specs/2026-11-13-opensandbox-sdk-phase2-design.md`

## Global Constraints

- CI 门槛：`make pre-commit` 全绿；commit 一律 `git commit -s`（DCO）。
- graceful delete 行为**零变化**（`Shutdown(true)` + `ReleaseAllIdleParallel`）。
- `forceDeleteTombstoneTTL = 5 * time.Minute`（新常量；SDK 默认 7 天会阻断同 ID 重建）。
- ExecdClient **不加** retry（避免命令重复执行）；只有 lifecycle client 加。
- examples 无第三方依赖：go 用 `net/http`，js 用原生 `fetch`。
- 每 task 一个 commit，conventional commits + `-s`。

## Overview

Phase 2 of issue #85: replace hand-rolled opensandbox adapter machinery with the SDK's own primitives where they are strictly better, and close the examples gap. Force delete adopts `SandboxPoolManager.Destroy` (cross-replica fence, store-level drain, bounded tombstone); the lifecycle client gains the SDK's default retry policy; the redundant `endpointHeaders` cache is deleted (the SDK client caches endpoints internally); and Go/JavaScript warm pool examples join the Python one in the E2E runner's live execution.

## Phase Mapping (repo design-doc template)

- **Phase 1 — Foundation**: Task 1 (PoolManager wiring: explicit shared state store, `poolDestroyer` seam, tombstone-bounded Destroy, `PoolDestroyedError` mapping)
- **Phase 2 — Implementation**: Task 2 (lifecycle retry), Task 3 (endpointHeaders removal)
- **Phase 3 — Integration**: Task 4 (go/js examples wired into the kind E2E runner against the real server)
- **Phase 4 — Documentation**: folded into each task per repo convention — AGENTS.md updated in Task 3's commit and the final-review fix wave (`fd70733`, `42c7382`); `configs/e2bgateway-example.yaml` N/A (no config surface change — Non-goal in spec §5); `docs/api/openapi.yaml` N/A (no HTTP API change — spec §4); spec/plan docs committed up front (`79222a9`, `deaae38`)

---

### Task 1: PoolManager.Destroy 接入 force delete

**Files:**
- Modify: `internal/adapter/opensandbox/warmpool.go`（`buildPoolStateStore`、`poolEntry`、`defaultNewPool`、`DeleteWarmPool`、`CreateWarmPool`、adapter `newPoolFn` 字段签名）
- Modify: `internal/adapter/opensandbox/adapter.go`（`newPoolFn` 字段类型）
- Test: `internal/adapter/opensandbox/warmpool_test.go`

**Interfaces:**
- Produces:
  - `type poolDestroyer interface { Destroy(ctx context.Context, poolName string, options opensandbox.PoolDestroyOptions) (*opensandbox.PoolDestroyResult, error) }`
  - `poolEntry.destroyer poolDestroyer` 字段
  - `const forceDeleteTombstoneTTL = 5 * time.Minute`
  - `newPoolFn` 新签名：`func(ctx context.Context, poolID, imageURI string, size int) (warmPool, poolDestroyer, error)`
  - `buildPoolStateStore(imageURI string)` **不再返回 nil**：memory 配置返回 `opensandbox.NewInMemoryPoolStateStore()`

**已核实事实**：`Destroy` 仅接受 `PoolDestroyForce`；`PoolDestroyOptions{Strategy, DrainTimeout *time.Duration, TombstoneTTL *time.Duration}`；`PoolDestroyResult{PoolName, State, DrainedIdleCount, KilledIdleCount}`；`DefaultSandboxPool.Start()` 对被 tombstone 的 namespace 返回 `*PoolDestroyedError`（pool.go:101 ensureNamespaceActive）；`GetDestroyState` 对过期 tombstone 读回 ACTIVE。

- [ ] **Step 1: Q1 核实（编码前置）**

Run: `grep -n "StateStore == nil\|NewInMemoryPoolStateStore" "$(go env GOMODCACHE)/github.com/alibaba/!open!sandbox/sdks/sandbox/go@v1.2.1/pool_builder.go"`
Expected: `Build()` 在 StateStore 为 nil 时默认 `NewInMemoryPoolStateStore()`——即显式传入与 nil 等价。若不等价（有额外分支），停下评估并在 spec Q1 记录。

- [ ] **Step 2: 写失败测试**

`warmpool_test.go` 新增 fake 与测试；`newPoolTestAdapter` 改为返回 destroyer 并更新所有调用点：

```go
// fakeDestroyer implements poolDestroyer for tests.
type fakeDestroyer struct {
	mu     sync.Mutex
	names  []string
	opts   []opensandbox.PoolDestroyOptions
	err    error
	result *opensandbox.PoolDestroyResult
}

func (f *fakeDestroyer) Destroy(_ context.Context, name string, opts opensandbox.PoolDestroyOptions) (*opensandbox.PoolDestroyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.names = append(f.names, name)
	f.opts = append(f.opts, opts)
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &opensandbox.PoolDestroyResult{PoolName: name, DrainedIdleCount: 2, KilledIdleCount: 2}, nil
}

func (f *fakeDestroyer) snapshot() ([]string, []opensandbox.PoolDestroyOptions) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.names...), append([]opensandbox.PoolDestroyOptions(nil), f.opts...)
}
```

`newPoolTestAdapter` 改造（签名加返回值）：

```go
func newPoolTestAdapter(fake *fakeWarmPool) (*Adapter, *fakeDestroyer) {
	dest := &fakeDestroyer{}
	a := &Adapter{ ...同现状... }
	a.newPoolFn = func(context.Context, string, string, int) (warmPool, poolDestroyer, error) {
		return fake, dest, nil
	}
	return a, dest
}
```

既有调用点批量改：`a := newPoolTestAdapter(fake)` → `a, _ := newPoolTestAdapter(fake)`（sed 完成后逐个核对编译）。

新测试：

```go
func TestDeleteWarmPool_ForceUsesPoolManagerDestroy(t *testing.T) {
	fake := &fakeWarmPool{}
	a, dest := newPoolTestAdapter(fake)
	createTestPool(t, a, "wp-fd")

	require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-fd",
		&adapter.WarmPoolDeleteOptions{Force: true}))

	assert.Equal(t, []bool{false}, fake.shutdownGracefulFlags(), "force shuts down non-gracefully")
	names, opts := dest.snapshot()
	require.Equal(t, []string{"wp-fd"}, names, "force delete must Destroy the pool namespace")
	assert.Equal(t, opensandbox.PoolDestroyForce, opts[0].Strategy)
	require.NotNil(t, opts[0].TombstoneTTL)
	assert.Equal(t, forceDeleteTombstoneTTL, *opts[0].TombstoneTTL,
		"tombstone TTL must be the short gateway value, not the 7-day SDK default")
	assert.Zero(t, fake.releases, "Destroy replaces ReleaseAllIdleParallel on the force path")
}

func TestDeleteWarmPool_GracefulDoesNotDestroy(t *testing.T) {
	fake := &fakeWarmPool{}
	a, dest := newPoolTestAdapter(fake)
	createTestPool(t, a, "wp-gd")

	require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-gd", nil))

	assert.Equal(t, []bool{true}, fake.shutdownGracefulFlags())
	assert.Equal(t, 1, fake.releases, "graceful keeps the in-process drain")
	names, _ := dest.snapshot()
	assert.Empty(t, names, "graceful must not write a tombstone")
}

func TestDeleteWarmPool_DestroyErrorPropagates(t *testing.T) {
	fake := &fakeWarmPool{}
	a, dest := newPoolTestAdapter(fake)
	dest.err = errors.New("store down")
	createTestPool(t, a, "wp-err")

	err := a.DeleteWarmPool(context.Background(), "wp-err",
		&adapter.WarmPoolDeleteOptions{Force: true})
	assert.ErrorContains(t, err, "destroying warm pool")
}

func TestCreateWarmPool_RejectedByTombstone(t *testing.T) {
	fake := &fakeWarmPool{startErr: &opensandbox.PoolDestroyedError{PoolName: "wp-t"}}
	a, _ := newPoolTestAdapter(fake)

	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-t",
	})
	assert.ErrorContains(t, err, "force-deleted recently")
}
```

`TestBuildPoolStateStore` 的 memory 断言从 `assert.Nil(store)` 改为 `assert.NotNil(store)`（显式 InMemory store），注释说明 pool 与 PoolManager 必须共享同一实例。

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./internal/adapter/opensandbox/ -run "TestDeleteWarmPool_Force|TestDeleteWarmPool_Graceful|TestCreateWarmPool_RejectedByTombstone" -v`
Expected: 编译失败（`poolDestroyer`/`forceDeleteTombstoneTTL`/新 newPoolFn 签名未定义）。

- [ ] **Step 4: 实现**

`warmpool.go`：

```go
// poolDestroyer is the adapter-facing subset of
// *opensandbox.SandboxPoolManager. Destroy fences the pool namespace across
// every peer sharing the state store, drains and kills idle sandboxes, and
// writes a DESTROYED tombstone. Tests substitute fakes.
type poolDestroyer interface {
	Destroy(ctx context.Context, poolName string,
		options opensandbox.PoolDestroyOptions) (*opensandbox.PoolDestroyResult, error)
}

// forceDeleteTombstoneTTL bounds how long a force-deleted pool namespace
// refuses rebinding. The SDK default (7 days) would block intentional
// same-ID recreation far too long; 5 minutes covers the concurrent-teardown
// window while letting an administrator recreate the pool soon after.
const forceDeleteTombstoneTTL = 5 * time.Minute
```

`poolEntry` 加字段：

```go
	// destroyer retires the pool's state-store namespace on force delete
	// (fence + drain + tombstone). Shares the pool's state store instance.
	destroyer poolDestroyer
```

`buildPoolStateStore` memory 分支改为：

```go
	if a.poolStoreCfg == nil || a.poolStoreCfg.Type == "" || a.poolStoreCfg.Type == storeTypeMemory {
		// Explicit (not nil) so the pool and its PoolManager share the same
		// store instance — the manager's fence/tombstone must be visible to
		// the pool's reconcile loop.
		return opensandbox.NewInMemoryPoolStateStore(), nil
	}
```

`defaultNewPool`：store 必非 nil；构造 manager 并随 pool 返回：

```go
	store, err := a.buildPoolStateStore(imageURI)
	if err != nil {
		return nil, nil, err
	}
	b = b.StateStore(store)
	p, err := b.Build()
	if err != nil {
		return nil, nil, fmt.Errorf("building pool %q: %w", poolID, err)
	}
	mgr, err := opensandbox.NewSandboxPoolManagerBuilder().
		StateStore(store).
		ConnectionConfig(cc).
		Build()
	if err != nil {
		return nil, nil, fmt.Errorf("building pool manager %q: %w", poolID, err)
	}
	return &sdkWarmPool{inner: p}, mgr, nil
```

adapter.go `newPoolFn` 字段与 `CreateWarmPool` 调用点随新签名更新；`CreateWarmPool` 的 entry 构造加 `destroyer: destroyer`；`pool.Start` 错误分支前插：

```go
	var destroyed *opensandbox.PoolDestroyedError
	if errors.As(err, &destroyed) {
		_ = pool.Shutdown(ctx, false)
		return nil, fmt.Errorf("warm pool %q was force-deleted recently; retry after the tombstone expires (~%v) or choose a different warmPoolID", id, forceDeleteTombstoneTTL)
	}
```

`DeleteWarmPool` teardown 段改为（保留既有 Shutdown 注释，追加 Destroy 说明）：

```go
	if force {
		slog.Info("force-deleting warm pool", "adapter", a.name, "warmPoolID", warmPoolID)
	}
	shutdownErr := entry.pool.Shutdown(ctx, !force)

	var destroyErr error
	var released int
	var releaseErr error
	if force && entry.destroyer != nil {
		// Store-level teardown: fences every peer sharing the state store
		// (no further replenishment), drains and kills idle sandboxes, and
		// tombstones the namespace so it cannot silently rebind. Retry-safe
		// per SDK contract; the tombstone expires after
		// forceDeleteTombstoneTTL.
		ttl := forceDeleteTombstoneTTL
		var res *opensandbox.PoolDestroyResult
		res, destroyErr = entry.destroyer.Destroy(ctx, warmPoolID, opensandbox.PoolDestroyOptions{
			Strategy:     opensandbox.PoolDestroyForce,
			TombstoneTTL: &ttl,
		})
		if destroyErr == nil {
			slog.Info("warm pool namespace destroyed",
				"adapter", a.name, "warmPoolID", warmPoolID,
				"drainedIdle", res.DrainedIdleCount, "killedIdle", res.KilledIdleCount)
		}
	} else {
		released, releaseErr = entry.pool.ReleaseAllIdleParallel(ctx, poolReleaseWorkers)
	}

	if shutdownErr != nil {
		return fmt.Errorf("shutting down warm pool %q: %w", warmPoolID, shutdownErr)
	}
	if destroyErr != nil {
		// PoolDestroyIncompleteError leaves the namespace DESTROYING; retrying
		// Destroy is safe per the SDK contract (spec §7 observability).
		slog.Warn("warm pool force destroy incomplete; namespace stays DESTROYING, retry is safe",
			"adapter", a.name, "warmPoolID", warmPoolID, "error", destroyErr)
		return fmt.Errorf("destroying warm pool %q: %w", warmPoolID, destroyErr)
	}
	if releaseErr != nil {
		return fmt.Errorf("releasing idle sandboxes of warm pool %q (%d released): %w", warmPoolID, released, releaseErr)
	}
	return nil
```

其余 newPoolFn 测试替身（如 `TestDefaultNewPool_Builds`、adapter_test 中直接赋值 newPoolFn 处）随新三元签名更新。

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/adapter/opensandbox/ -race -v -run "TestDeleteWarmPool|TestCreateWarmPool|TestBuildPoolStateStore|TestDefaultNewPool"`
Expected: 全部 PASS（含既有 Delete/Create 回归）。

- [ ] **Step 6: Commit**

```bash
go test ./... -race 2>&1 | grep -v "^ok" | head -5   # 全仓无失败
git add internal/adapter/opensandbox/
git commit -s -m "feat(opensandbox): force delete via SDK PoolManager.Destroy with bounded tombstone"
```

---

### Task 2: lifecycle client 启用 WithRetry

**Files:**
- Modify: `internal/adapter/opensandbox/adapter.go:167`（`New` 中 `NewLifecycleClient` 调用）
- Test: `internal/adapter/opensandbox/adapter_test.go`

**Interfaces:**
- Consumes: SDK `WithRetry(cfg RetryConfig) Option`、`DefaultRetryConfig() RetryConfig`（3 次重试、500ms 起步、2x、30s 封顶；429/502/503/504；非幂等请求仅在"字节未发出"的传输层失败时重试——SDK 内建保护）。
- Produces: gateway lifecycle 调用对瞬时 5xx/网络失败自动重试。

- [ ] **Step 1: 写失败测试**

`adapter_test.go` 追加（imports 视需要补 `net/http`、`net/http/httptest`、`sync/atomic`）：

```go
func TestLifecycleClientRetriesTransientFailures(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sandboxes" && hits.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"pagination":{"page":1,"pageSize":1,"totalItems":0,"totalPages":0,"hasNextPage":false}}`))
	}))
	defer srv.Close()

	a, err := New(AdapterConfig{Name: "opensandbox", BaseURL: srv.URL + "/v1"})
	require.NoError(t, err)
	require.NoError(t, a.HealthCheck(context.Background()),
		"HealthCheck should survive transient 503s via SDK retry")
	assert.GreaterOrEqual(t, hits.Load(), int32(3), "expected at least 2 retries before success")
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/adapter/opensandbox/ -run TestLifecycleClientRetriesTransientFailures -v`
Expected: FAIL——无 retry 时首个 503 即让 HealthCheck 报错。

- [ ] **Step 3: 实现**

adapter.go:167：

```go
	lifecycle := opensandbox.NewLifecycleClient(cfg.BaseURL, cfg.APIKey,
		opensandbox.WithRetry(opensandbox.DefaultRetryConfig()))
```

（注释说明：仅 lifecycle client；ExecdClient 不加重试以免命令重复执行。）

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/adapter/opensandbox/ -race -run "TestLifecycleClientRetries|TestNew_" -v`
Expected: PASS（重试 2 次后成功；测试耗时约 1.5s 属预期 backoff）。

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/opensandbox/adapter.go internal/adapter/opensandbox/adapter_test.go
git commit -s -m "feat(opensandbox): enable SDK default retry on the lifecycle client"
```

---

### Task 3: 删除 endpointHeaders 手工缓存

**Files:**
- Modify: `internal/adapter/opensandbox/adapter.go`（struct 字段 L82-84、初始化 L193、`getOrCreateExecdClient` L275-292 merge 块、Set ×3：L1025 / L1085 / L1268 及其伴随注释 L243、L1066、L1243）
- Test: `internal/adapter/opensandbox/adapter_test.go`（引用点清理）

**Interfaces:**
- Consumes: 无（纯删除）。
- Produces: `getOrCreateExecdClient` 只用当次 `ep.Headers`；行为不变式见 spec §3.4。

- [ ] **Step 1: 找测试引用点**

Run: `grep -rn "endpointHeaders" internal/ --include="*_test.go"`
记录需要同步删除/调整的断言（若测试直接断言缓存内容，改为断言 ExecdClient 行为或删除该断言并注明原因）。

- [ ] **Step 2: 删除实现**

- struct：删除 `endpointHeaders *cache.Cache` 字段与注释（L82-84）。
- `New()`：删除 `endpointHeaders: cache.New(10000, 1*time.Hour),`（L193）。
- `getOrCreateExecdClient`（L275-292）：删除 mergedHeaders 构造与 `a.endpointHeaders.Get`，替换为：

```go
	// Server-returned headers (auth cookies, routing hints) travel with the
	// endpoint response itself; the SDK's internal EndpointCache already
	// reuses unsigned GetEndpoint results for 600s, so no gateway-side
	// header cache is needed.
	var opts []opensandbox.Option
	if len(ep.Headers) > 0 {
		opts = append(opts, opensandbox.WithHeaders(ep.Headers))
	}
```

- L1025（GetPortURL）：删除 `a.endpointHeaders.Set(...)` 及所属 if 块（该 Set 的 key 为 `{id}-{port}`，从未被任何 Get 命中——死写入）。
- L1085（GetAccessToken）、L1268（GetEnvdEndpoint）：删除 `a.endpointHeaders.Set(sandboxID, ep.Headers)` 及伴随注释行（"Store server-returned headers ..." / "stored in the endpointHeaders cache for later use by ExecdClient"）。
- L243 注释：更新为不再提及 endpointHeaders 缓存。
- `maps` import 若因此不再被使用则删除（`goimports` 校验）。

- [ ] **Step 3: 编译 + 全量测试**

Run: `go build ./... && go test ./internal/adapter/opensandbox/ -race`
Expected: 编译通过（无残留引用）、测试全绿。若测试红：区分"断言缓存本身"（删断言）与"行为回归"（停下排查，不得为绿而弱化行为断言）。

- [ ] **Step 4: Commit**

```bash
git add internal/adapter/opensandbox/
git commit -s -m "refactor(opensandbox): drop redundant endpointHeaders cache (SDK EndpointCache covers it)"
```

---

### Task 4: go/js warm pool 示例 + runner 纳入 CI

**Files:**
- Create: `examples/go/warm_pools/main.go`
- Create: `examples/javascript/warm_pools.js`
- Modify: `hack/kind-e2e/run-examples-e2e.sh`（go loop 列表 + HTTP-only 段加 node）
- Modify: `examples/README.md`（示例清单加两行，格式随现有）

**Interfaces:**
- Consumes: gateway REST（与 python 版一致）：`GET/POST /warm-pools`、`GET /warm-pools/{id}`、`POST /warm-pools/{id}/size`、`DELETE /warm-pools/{id}[?force=true]`；响应字段 `warmPoolID`/`templateID`/`targetSize`/`currentSize`/`status`。
- Produces: 两个示例失败时非零退出（runner 以退出码判 pass/fail）；成功输出含 `Done.`（go loop 的既有 grep 断言 `Killed|Done|killed|Created` 已覆盖）。

- [ ] **Step 1: 写 go 示例**

`examples/go/warm_pools/main.go`（license header 抄 hello_world；风格一致）：

```go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

type WarmPoolCreateRequest struct {
	TemplateID string `json:"templateID"`
	TargetSize int    `json:"targetSize"`
}

type WarmPoolInfo struct {
	WarmPoolID  string `json:"warmPoolID"`
	TemplateID  string `json:"templateID"`
	TargetSize  int    `json:"targetSize"`
	CurrentSize int    `json:"currentSize"`
	Status      string `json:"status"`
}

var (
	baseURL = envOr("E2B_DOMAIN", "localhost:8080")
	apiKey  = envOr("E2B_API_KEY", "test-key")
	client  = &http.Client{Timeout: 120 * time.Second}
	created []string // pools to best-effort clean up on failure
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func do(method, path string, body any, query map[string]string) (int, []byte, error) {
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, baseURL+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	q := req.URL.Query()
	for k, v := range query {
		q.Set(k, v)
	}
	req.URL.RawQuery = q.Encode()
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes(), nil
}

func must(status int, body []byte, err error, step string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", step, err)
		cleanup()
		os.Exit(1)
	}
	if status >= 400 {
		fmt.Fprintf(os.Stderr, "%s: HTTP %d: %s\n", step, status, body)
		cleanup()
		os.Exit(1)
	}
}

func cleanup() {
	for _, id := range created {
		_, _, _ = do(http.MethodDelete, "/warm-pools/"+id, nil, map[string]string{"force": "true"})
	}
}

func main() {
	if !startsWithHTTP(baseURL) {
		baseURL = "http://" + baseURL
	}

	fmt.Println("1. Listing warm pools...")
	status, body, err := do(http.MethodGet, "/warm-pools", nil, nil)
	must(status, body, err, "list")
	var pools []WarmPoolInfo
	_ = json.Unmarshal(body, &pools)
	fmt.Printf("   Existing pools: %d\n", len(pools))

	fmt.Println("2. Creating warm pool...")
	status, body, err = do(http.MethodPost, "/warm-pools",
		WarmPoolCreateRequest{TemplateID: "base", TargetSize: 3}, nil)
	must(status, body, err, "create")
	var pool WarmPoolInfo
	_ = json.Unmarshal(body, &pool)
	created = append(created, pool.WarmPoolID)
	fmt.Printf("   Pool: %s (template=%s, target=%d)\n", pool.WarmPoolID, pool.TemplateID, pool.TargetSize)

	fmt.Println("3. Getting warm pool details...")
	status, body, err = do(http.MethodGet, "/warm-pools/"+pool.WarmPoolID, nil, nil)
	must(status, body, err, "get")
	var detail WarmPoolInfo
	_ = json.Unmarshal(body, &detail)
	fmt.Printf("   Status: %s, target=%d, current=%d\n", detail.Status, detail.TargetSize, detail.CurrentSize)

	fmt.Println("4. Updating pool size...")
	status, body, err = do(http.MethodPost, "/warm-pools/"+pool.WarmPoolID+"/size",
		map[string]int{"targetSize": 5}, nil)
	must(status, body, err, "resize")
	fmt.Printf("   Updated: HTTP %d\n", status)

	fmt.Println("5. Deleting warm pool (graceful)...")
	status, body, err = do(http.MethodDelete, "/warm-pools/"+pool.WarmPoolID, nil, nil)
	must(status, body, err, "delete")
	created = created[:0]
	fmt.Printf("   Deleted: HTTP %d\n", status)

	fmt.Println("6. Force-deleting a warm pool...")
	status, body, err = do(http.MethodPost, "/warm-pools",
		WarmPoolCreateRequest{TemplateID: "base", TargetSize: 1}, nil)
	must(status, body, err, "create-for-force")
	var forcePool WarmPoolInfo
	_ = json.Unmarshal(body, &forcePool)
	created = append(created, forcePool.WarmPoolID)
	status, body, err = do(http.MethodDelete, "/warm-pools/"+forcePool.WarmPoolID, nil,
		map[string]string{"force": "true"})
	must(status, body, err, "force-delete")
	created = created[:0]
	fmt.Printf("   Force deleted %s: HTTP %d\n", forcePool.WarmPoolID, status)

	fmt.Println("\nDone.")
}

func startsWithHTTP(s string) bool {
	return len(s) >= 7 && (s[:7] == "http://" || (len(s) >= 8 && s[:8] == "https://"))
}
```

注：`main` 开头的 `startsWithHTTP` 判断与 python 版语义一致——E2B_DOMAIN 不以 http 开头才补 `http://` 前缀，用户自带 scheme 时不会双前缀。

- [ ] **Step 2: 写 js 示例**

`examples/javascript/warm_pools.js`（原生 fetch，无 npm 依赖）：

```javascript
/**
 * Warm Pools - manage pre-warmed sandbox pools over the gateway REST API.
 *
 * Uses native fetch (Node >= 18); no npm dependencies (warm pools are not
 * part of the E2B SDK surface).
 *
 * Demonstrates:
 * 1. List warm pools
 * 2. Create a warm pool
 * 3. Get warm pool details
 * 4. Update pool size
 * 5. Delete warm pool (graceful)
 * 6. Force delete (?force=true — immediate teardown)
 *
 * Usage:
 *   E2B_DOMAIN=localhost:8080 E2B_API_KEY=test-key node warm_pools.js
 */

let baseUrl = process.env.E2B_DOMAIN || "localhost:8080";
if (!baseUrl.startsWith("http")) baseUrl = `http://${baseUrl}`;
const headers = {
  "X-API-Key": process.env.E2B_API_KEY || "test-key",
  "Content-Type": "application/json",
};

const created = []; // pools to best-effort clean up on failure

async function request(method, path, { body, query } = {}) {
  const url = new URL(path, baseUrl);
  for (const [k, v] of Object.entries(query || {})) url.searchParams.set(k, v);
  const resp = await fetch(url, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!resp.ok) {
    throw new Error(`${method} ${path} -> HTTP ${resp.status}: ${await resp.text()}`);
  }
  return resp.status === 204 ? null : resp.json();
}

async function main() {
  console.log("1. Listing warm pools...");
  const pools = await request("GET", "/warm-pools");
  console.log(`   Existing pools: ${pools.length}`);

  console.log("2. Creating warm pool...");
  const pool = await request("POST", "/warm-pools", {
    body: { templateID: "base", targetSize: 3 },
  });
  created.push(pool.warmPoolID);
  console.log(`   Pool: ${pool.warmPoolID} (template=${pool.templateID}, target=${pool.targetSize})`);

  console.log("3. Getting warm pool details...");
  const detail = await request("GET", `/warm-pools/${pool.warmPoolID}`);
  console.log(`   Status: ${detail.status}, target=${detail.targetSize}, current=${detail.currentSize}`);

  console.log("4. Updating pool size...");
  await request("POST", `/warm-pools/${pool.warmPoolID}/size`, { body: { targetSize: 5 } });
  console.log("   Updated");

  console.log("5. Deleting warm pool (graceful)...");
  await request("DELETE", `/warm-pools/${pool.warmPoolID}`);
  created.length = 0;
  console.log("   Deleted");

  console.log("6. Force-deleting a warm pool...");
  const forcePool = await request("POST", "/warm-pools", {
    body: { templateID: "base", targetSize: 1 },
  });
  created.push(forcePool.warmPoolID);
  await request("DELETE", `/warm-pools/${forcePool.warmPoolID}`, { query: { force: "true" } });
  created.length = 0;
  console.log(`   Force deleted ${forcePool.warmPoolID}`);

  console.log("\nDone.");
}

main().catch(async (err) => {
  console.error("ERROR:", err.message);
  for (const id of created) {
    try {
      await request("DELETE", `/warm-pools/${id}`, { query: { force: "true" } });
    } catch {
      /* best-effort */
    }
  }
  process.exit(1);
});
```

- [ ] **Step 3: 本地静态校验**

```bash
cd examples/go/warm_pools && go vet . && go build -o /tmp/wp-ex . && echo "go OK"
node --check ../../javascript/warm_pools.js && echo "js OK"
```

（活体验证由 CI runner 执行；若本地有 gateway+server 环境可按 Phase 1 Task 4 Step 5 方式跑通后删除产物。）

- [ ] **Step 4: runner 纳入 CI**

`hack/kind-e2e/run-examples-e2e.sh`：

1. go loop 列表：`for ex in hello_world sandbox_lifecycle filesystem coding_agent; do` → `for ex in hello_world sandbox_lifecycle filesystem coding_agent warm_pools; do`
2. HTTP-only 段改名并追加 node 执行（python 循环之后、`fi` 之前）：

```bash
  for ex in warm_pools.js; do
    echo "--- Node(HTTP): $ex ---"
    if E2B_DOMAIN="${E2B_DOMAIN}" E2B_API_KEY="${E2B_API_KEY}" \
       timeout 300 node ./examples/javascript/${ex} 2>&1 | tee /tmp/js-http-${ex}.log | tail -8; then
      if [ "${PIPESTATUS[0]}" = "0" ]; then
        pass "Node(HTTP): $ex"
      else
        fail "Node(HTTP): $ex" "exit code non-zero (see /tmp/js-http-${ex}.log)"
      fi
    else
      fail "Node(HTTP): $ex" "timeout"
    fi
  done
```

段标题 `=== Python HTTP-only Examples ===` 改为 `=== HTTP-only Examples (python + node) ===`；requests 安装失败的 fail 分支同时覆盖 node 段（node 由 setup-node 提供，无安装步骤）。

3. `examples/README.md` 的 Examples 清单按现有格式补 `warm_pools`（go/python/js 三行或对应表格行）。

- [ ] **Step 5: bash 校验 + Commit**

```bash
bash -n hack/kind-e2e/run-examples-e2e.sh && echo OK
git add examples/ hack/kind-e2e/run-examples-e2e.sh
git commit -s -m "feat(examples): add go and javascript warm pool examples, run them in E2E (#85)"
```

---

### Task 5: 收尾验证 + PR

- [ ] **Step 1: 全量门槛**

Run: `make pre-commit`
Expected: exit 0。

- [ ] **Step 2: push + PR**

```bash
git push -u origin opensandbox-sdk-phase2
```

按 submit-pr skill 开 PR：
- 标题：`feat(opensandbox): adopt SDK PoolManager destroy, retry, and drop redundant caches (#85)`
- Body：Summary（Phase 2 范围）、Changes（Destroy/tombstone TTL 决策、WithRetry、endpointHeaders 删除论证、示例）、Testing、Documentation（spec/plan 链接）、Checklist；标注 **Closes #85**（Phase 1 已随 #87 合入，本 PR 完成剩余工作项）。

- [ ] **Step 3: 监视 CI + CodeRabbit**

后台 watch `gh pr checks`；E2E (opensandbox) 的 live warm pool 段将实证 Destroy 路径（force delete → 204 → claimed 存活）；出现评论按 receiving-code-review 流程处理。

- [ ] **Step 4: 对照 issue #85 验收清单**

- PoolManager 复用 ✅（Task 1）；EndpointCache 结论落地（删除冗余缓存）✅（Task 3）；RetryConfig 采纳 ✅（Task 2）
- go/js 示例 ✅（Task 4）；Phase 1 项已由 #87 完成
- #85 可关闭

---

## Checklist

- [ ] Spec reviewed（`2026-11-13-opensandbox-sdk-phase2-design.md`；Q3 已解决，Q1 在 Task 1 Step 1 核实，Q2 已核实为 `DefaultRetryConfig()`）
- [ ] graceful 路径零行为变化（单测断言 destroyer 不被调用）
- [ ] tombstone TTL = 5m 且 CreateWarmPool 对 PoolDestroyedError 给出可操作错误
- [ ] ExecdClient 无 retry
- [ ] `endpointHeaders` 字段及全部 Set/Get 删除，无残留引用
- [ ] go/js 示例在两个 E2E job 活体执行并通过
- [ ] `make pre-commit` 全绿；所有 commit 带 Signed-off-by
- [ ] Unit tests pass with `-race`（repo template item）
- [ ] Integration tests for each affected adapter — N/A：仅 opensandbox adapter 受影响，其集成面由 kind E2E（真实 server）+ 包内 `-race` 单测覆盖；agentsandbox/e2bcloud/mock 无签名或行为变更
- [ ] E2E tests added and passing（runner live 段 + go/js/py 示例活体执行）
- [ ] `AGENTS.md` updated（endpointHeaders 移除、real-server E2E 措辞、header 转发范围限定 ExecdClient）
- [ ] Config example updated — N/A：无配置 schema 变更（spec §5 Non-goal）
- [ ] OpenAPI docs updated — N/A：无 HTTP API 变更（spec §4）
- [ ] No new `gocyclo` warnings；`golangci-lint` clean
