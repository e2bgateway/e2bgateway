# Warm Pool 修复实现计划（PR #82 遗留评审 A1-A4 + Issue #80 force delete）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复 PR #82 的 4 条未解决 coderabbit 评审意见（路由刷新退避、删除后清缓存路由、Redis TLS、idle 状态按镜像隔离），并实现 issue #80 的 warm pool 强制删除（`DELETE /warm-pools/{id}?force=true`）。

**Architecture:** A1/A2 是 agentsandbox adapter 内部小修；A3/A4 是 opensandbox adapter 的 Redis 层加固；#80 扩展 `SandboxAdapter.DeleteWarmPool` 签名为 `(ctx, id, opts *WarmPoolDeleteOptions)`，四个 adapter + test/mock + stub 全部同步，handler 解析 query param。

**Tech Stack:** Go 1.26+、testify、k8s fake clientsets（`extfake`/`agentsfake`）、go-redis v9、OpenSandbox SDK v1.2.0、agent-sandbox v1.0.10。

**Spec:** `docs/superpowers/specs/2026-11-13-warmpool-fixes-design.md`

## Global Constraints

- CI 门槛：`make pre-commit`（fmt + vet + lint + test），单测跑 `-race`。
- `SandboxAdapter` 接口变更必须更新**所有**实现：`internal/adapter/{agentsandbox,opensandbox,e2bcloud,mock}`、`test/mock/backend.go`、`internal/api/v1/ws_handler_test.go` 的 `stubAdapter`。
- Viper 小写化 map key：配置解析用既有 `lookupString`/`util.LookupBool` helper，key 同时给小写与 camelCase 变体（单段小写 key 如 `tls` 无变体问题）。
- 不提供 `insecureSkipVerify` / 自定义 CA（spec Non-goal）。
- 代码注释风格：解释 **why**，与周边代码密度一致；license header 不动。
- 每个 task 结束提交一次 commit；commit message 用 conventional commits（`fix(adapter):` / `feat(adapter):` / `feat(api):`）。

---

### Task 1: A2 — agentsandbox DeleteWarmPool 先清缓存路由

**Files:**
- Modify: `internal/adapter/agentsandbox/warmpool.go:159-180`（`DeleteWarmPool`）
- Test: `internal/adapter/agentsandbox/warmpool_test.go`

**Interfaces:**
- Consumes: 现有 `newWarmPoolTestAdapter(t, objs...) (*Adapter, *extfake.Clientset)`、`warmPoolCRD(name, templateRef, replicas, created)` test helper。
- Produces: 行为变更——`DeleteWarmPool` 成功后 `warmPoolMap` 中不再有 value == 被删池 ID 的条目（即使后续 refresh 失败）。Task 6 的 force 路径复用此逻辑（同函数内）。

- [ ] **Step 1: 写失败测试**

追加到 `internal/adapter/agentsandbox/warmpool_test.go`（新增 imports：`errors`、`k8s.io/client-go/testing` 别名 `k8stesting`；文件已有 `apierrors`、`runtime`）：

```go
func TestDeleteWarmPool_DropsCachedRoutesWhenRefreshFails(t *testing.T) {
	a, cs := newWarmPoolTestAdapter(t, warmPoolCRD("wp-1", "base", 2, time.Now()))
	a.warmPoolMap["base"] = "wp-1"

	// Fail the post-delete routing refresh (List). The delete itself uses the
	// "delete" verb and stays successful.
	cs.PrependReactor("list", "sandboxwarmpools", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(errors.New("list boom"))
	})

	require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-1"))
	_, stillRouted := a.warmPoolMap["base"]
	assert.False(t, stillRouted,
		"cache must not keep pointing at the deleted pool when the refresh fails")
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/adapter/agentsandbox/ -run TestDeleteWarmPool_DropsCachedRoutesWhenRefreshFails -v`
Expected: FAIL —— `stillRouted == true`（现状 refresh 失败时缓存保留）。

- [ ] **Step 3: 实现**

`DeleteWarmPool` 中，CRD `Delete` 成功之后、`a.refreshWarmPoolRouting(ctx)` 之前插入：

```go
	// Drop cached routes to the deleted pool BEFORE the best-effort refresh:
	// if the List below fails, the cache must not keep pointing claims at a
	// pool that no longer exists (the controller would answer
	// WarmPoolNotFound). Config overrides for affected templates are restored
	// by the next successful refresh. warmPoolOpsMu is already held, so this
	// cannot interleave with a concurrent create's publication.
	a.warmPoolMapMu.Lock()
	for tmpl, wp := range a.warmPoolMap {
		if wp == warmPoolID {
			delete(a.warmPoolMap, tmpl)
		}
	}
	a.warmPoolMapMu.Unlock()
```

同时把原有 refresh 调用处的旧注释（"Rebuild routing from the remaining CRDs…"）保留在 `a.refreshWarmPoolRouting(ctx)` 上方。

- [ ] **Step 4: 跑测试确认通过（含既有回归）**

Run: `go test ./internal/adapter/agentsandbox/ -race -v -run "TestDeleteWarmPool|TestRestoreWarmPoolRouting|TestWarmPoolCRUD"`
Expected: 全部 PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/agentsandbox/warmpool.go internal/adapter/agentsandbox/warmpool_test.go
git commit -m "fix(adapter): drop cached routes to deleted warm pool before best-effort refresh"
```

---

### Task 2: A1 — agentsandbox 路由刷新失败退避

**Files:**
- Modify: `internal/adapter/agentsandbox/adapter.go`（struct ~line 97-100、`resolveWarmPool` ~line 873-890）
- Modify: `internal/adapter/agentsandbox/warmpool.go`（`refreshWarmPoolRouting` ~line 221、常量区 ~line 49-53）
- Test: `internal/adapter/agentsandbox/warmpool_test.go`

**Interfaces:**
- Consumes: Task 1 后的 `DeleteWarmPool`（其中 refresh 调用不变）；`newWarmPoolTestAdapter`。
- Produces: 新字段 `warmPoolRoutingLastAttempt time.Time`（`warmPoolMapMu` 保护）、新常量 `warmPoolRoutingRetryBackoff = 30 * time.Second`。`refreshWarmPoolRouting` 无论成败都记录尝试时间。

- [ ] **Step 1: 写失败测试**

追加到 `warmpool_test.go`（新增 imports：`sync/atomic`；`k8stesting` 已在 Task 1 引入）：

```go
func TestResolveWarmPool_BackoffAfterFailedRefresh(t *testing.T) {
	a, cs := newWarmPoolTestAdapter(t)
	var lists atomic.Int32
	cs.PrependReactor("list", "sandboxwarmpools", func(k8stesting.Action) (bool, runtime.Object, error) {
		lists.Add(1)
		return true, nil, apierrors.NewInternalError(errors.New("list boom"))
	})
	// Make the routing cache stale so resolveWarmPool wants to refresh.
	a.warmPoolMapMu.Lock()
	a.warmPoolRoutingAt = time.Now().Add(-2 * warmPoolRoutingTTL)
	a.warmPoolMapMu.Unlock()

	_ = a.resolveWarmPool(context.Background(), "base")
	assert.Equal(t, int32(1), lists.Load(), "first stale resolve attempts a refresh")

	_ = a.resolveWarmPool(context.Background(), "base")
	assert.Equal(t, int32(1), lists.Load(),
		"refresh within the failure backoff window must not re-list synchronously")

	// Simulate the backoff window elapsing.
	a.warmPoolMapMu.Lock()
	a.warmPoolRoutingLastAttempt = time.Now().Add(-2 * warmPoolRoutingRetryBackoff)
	a.warmPoolMapMu.Unlock()

	_ = a.resolveWarmPool(context.Background(), "base")
	assert.Equal(t, int32(2), lists.Load(), "refresh retries once the backoff has elapsed")
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/adapter/agentsandbox/ -run TestResolveWarmPool_BackoffAfterFailedRefresh -v`
Expected: 编译失败（`a.warmPoolRoutingLastAttempt`、`warmPoolRoutingRetryBackoff` 未定义）。

- [ ] **Step 3: 实现**

`warmpool.go` 常量区（`warmPoolRoutingTTL` 之后）加：

```go
// warmPoolRoutingRetryBackoff spaces out routing refresh retries after a
// failed attempt. Without it a persistently failing List would leave
// warmPoolRoutingAt stale, making every CreateSandbox that acquires
// warmPoolOpsMu pay the restore timeout synchronously before falling back
// to the (still valid) cached route.
const warmPoolRoutingRetryBackoff = 30 * time.Second
```

`refreshWarmPoolRouting` 函数体开头（`List` 调用之前）加：

```go
	// Record the attempt (not just successes) so resolveWarmPool can back off
	// after failures. Successes additionally bump warmPoolRoutingAt below.
	a.warmPoolMapMu.Lock()
	a.warmPoolRoutingLastAttempt = time.Now()
	a.warmPoolMapMu.Unlock()
```

失败分支的 `slog.Warn` 增加字段 `"nextRetryIn", warmPoolRoutingRetryBackoff.String()`。

`adapter.go` struct 中 `warmPoolRoutingAt` 之后加字段：

```go
	// warmPoolRoutingLastAttempt is when the most recent routing refresh
	// attempt started, successful or not. resolveWarmPool skips refreshes
	// within warmPoolRoutingRetryBackoff of the last attempt so a failing
	// List cannot delay every sandbox creation. Guarded by warmPoolMapMu.
	warmPoolRoutingLastAttempt time.Time
```

`resolveWarmPool` 的 stale 判定改为（两个时间戳在同一次 RLock 内读）：

```go
	a.warmPoolMapMu.RLock()
	stale := time.Since(a.warmPoolRoutingAt) > warmPoolRoutingTTL &&
		time.Since(a.warmPoolRoutingLastAttempt) >= warmPoolRoutingRetryBackoff
	a.warmPoolMapMu.RUnlock()
```

注意：`warmPoolRoutingLastAttempt` 零值使 `time.Since` 极大，首次刷新与 `New()` 里的启动 refresh 行为不变。

- [ ] **Step 4: 跑测试确认通过（含回归）**

Run: `go test ./internal/adapter/agentsandbox/ -race -v`
Expected: 全部 PASS（若既有测试依赖"stale 即刷新"，检查其是否设置了 `warmPoolRoutingAt`；`newWarmPoolTestAdapter` 零值 lastAttempt 不阻碍刷新）。

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/agentsandbox/adapter.go internal/adapter/agentsandbox/warmpool.go internal/adapter/agentsandbox/warmpool_test.go
git commit -m "fix(adapter): back off failed warm pool routing refreshes"
```

---

### Task 3: A3 — Redis TLS（templateStore + pool stateStore，强制校验）

**Files:**
- Create: `internal/adapter/opensandbox/redis_tls.go`
- Create: `internal/adapter/opensandbox/redis_tls_test.go`
- Modify: `internal/adapter/opensandbox/redis_store.go`（`RedisConfig`、`NewRedisTemplateStore`）
- Modify: `internal/adapter/opensandbox/warmpool.go`（`PoolStateStoreConfig`、`buildPoolStateStore`）
- Modify: `internal/adapter/opensandbox/factory.go`（两处解析 `tls` key）
- Modify: `configs/e2bgateway-example.yaml`（stateStore 注释块）

**Interfaces:**
- Consumes: 既有 `lookupString`/`lookupInt`/`util.LookupBool` helper。
- Produces（Task 4 依赖）:
  - `func normalizeRedisAddr(addr string) (bareAddr string, schemeWantsTLS bool)`
  - `func redisTLSConfig(addr string, tlsEnabled, hasPassword bool) (*tls.Config, error)`
  - `RedisConfig.EnableTLS bool`、`PoolStateStoreConfig.EnableTLS bool`

- [ ] **Step 1: 写失败测试（helper 表驱动）**

创建 `redis_tls_test.go`（license header 抄同目录其它文件）：

```go
package opensandbox

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeRedisAddr(t *testing.T) {
	tests := []struct {
		in       string
		wantAddr string
		wantTLS  bool
	}{
		{"redis.example.com:6379", "redis.example.com:6379", false},
		{"rediss://redis.example.com:6379", "redis.example.com:6379", true},
		{"localhost:6379", "localhost:6379", false},
	}
	for _, tt := range tests {
		addr, schemeTLS := normalizeRedisAddr(tt.in)
		assert.Equal(t, tt.wantAddr, addr)
		assert.Equal(t, tt.wantTLS, schemeTLS)
	}
}

func TestRedisTLSConfig(t *testing.T) {
	tests := []struct {
		name        string
		addr        string
		tlsEnabled  bool
		hasPassword bool
		wantTLS     bool
		wantErr     bool
	}{
		{"no password no tls stays cleartext", "redis:6379", false, false, false, false},
		{"tls enabled returns config", "redis.example.com:6379", true, true, true, false},
		{"tls without password allowed", "redis.example.com:6379", true, false, true, false},
		{"password to remote without tls is rejected", "redis.example.com:6379", false, true, false, true},
		{"password to loopback without tls is allowed", "127.0.0.1:6379", false, true, false, false},
		{"password to localhost without tls is allowed", "localhost:6379", false, true, false, false},
		{"password to ipv6 loopback without tls is allowed", "[::1]:6379", false, true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := redisTLSConfig(tt.addr, tt.tlsEnabled, tt.hasPassword)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.wantTLS {
				require.NotNil(t, cfg)
				assert.Equal(t, "redis.example.com", cfg.ServerName)
			} else {
				assert.Nil(t, cfg)
			}
		})
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/adapter/opensandbox/ -run "TestNormalizeRedisAddr|TestRedisTLSConfig" -v`
Expected: 编译失败（函数未定义）。

- [ ] **Step 3: 实现 helper**

创建 `redis_tls.go`（license header + `package opensandbox`）：

```go
import (
	"crypto/tls"
	"fmt"
	"net"
	"strings"
)

// redissPrefix is the addr scheme that implicitly enables TLS, mirroring the
// URL convention go-redis uses in ParseURL.
const redissPrefix = "rediss://"

// normalizeRedisAddr strips the rediss:// scheme from addr, reporting whether
// the scheme requested TLS.
func normalizeRedisAddr(addr string) (string, bool) {
	if rest, ok := strings.CutPrefix(addr, redissPrefix); ok {
		return rest, true
	}
	return addr, false
}

// isLoopbackHost reports whether host (bracket-stripped, port-less) refers to
// the local machine. Loopback connections are exempt from the TLS requirement
// so local development and miniredis-based tests keep working in cleartext.
func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// redisTLSConfig builds the TLS configuration for a Redis connection, shared
// by the templateStore and the warm pool stateStore.
//
// Enforcement (CWE-319, PR #82 review): a password-bearing connection to a
// non-loopback address MUST use TLS — enabled via "tls: true" or a rediss://
// addr. Anything else fails adapter construction instead of silently sending
// the credential in cleartext. Verification uses the system CA pool and the
// addr host as ServerName; there is deliberately no insecure-skip-verify
// escape hatch.
func redisTLSConfig(addr string, tlsEnabled, hasPassword bool) (*tls.Config, error) {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	if !tlsEnabled {
		if hasPassword && !isLoopbackHost(host) {
			return nil, fmt.Errorf(
				"redis addr %q is not loopback and a password is configured: "+
					"enable TLS (tls: true, or a rediss:// addr) to avoid sending "+
					"credentials in cleartext", addr)
		}
		return nil, nil
	}
	return &tls.Config{ServerName: strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"), MinVersion: tls.VersionTLS12}, nil
}
```

- [ ] **Step 4: 跑 helper 测试确认通过**

Run: `go test ./internal/adapter/opensandbox/ -run "TestNormalizeRedisAddr|TestRedisTLSConfig" -v`
Expected: PASS。

- [ ] **Step 5: 接线两个 store + 接线后测试**

`redis_store.go`：`RedisConfig` 加字段：

```go
	// EnableTLS turns on TLS for the Redis connection. Required (or use a
	// rediss:// Addr) when Password is set and Addr is not loopback.
	EnableTLS bool
```

`NewRedisTemplateStore` 中，`redis.NewClient` 之前：

```go
	addr, schemeTLS := normalizeRedisAddr(cfg.Addr)
	tlsCfg, err := redisTLSConfig(addr, cfg.EnableTLS || schemeTLS, cfg.Password != "")
	if err != nil {
		return nil, err
	}
```

`redis.NewClient(&redis.Options{...})` 改：`Addr: addr`（原 `cfg.Addr`），并加 `TLSConfig: tlsCfg`。

`warmpool.go`：`PoolStateStoreConfig` 加字段：

```go
	// EnableTLS turns on TLS for the Redis connection. Required (or use a
	// rediss:// Addr) when Password is set and Addr is not loopback.
	EnableTLS bool
```

`buildPoolStateStore` 中 `redis.NewClient` 段改为：

```go
	addr, schemeTLS := normalizeRedisAddr(a.poolStoreCfg.Addr)
	tlsCfg, err := redisTLSConfig(addr, a.poolStoreCfg.EnableTLS || schemeTLS, a.poolStoreCfg.Password != "")
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(&redis.Options{
		Addr:      addr,
		Password:  a.poolStoreCfg.Password,
		DB:        a.poolStoreCfg.DB,
		TLSConfig: tlsCfg,
	})
```

`factory.go` 两处解析（注意 `util` 已在 import 中）：

- `buildRedisStore`（templateStore，`redisCfg.KeyPrefix` 解析附近）加：

```go
	redisCfg.EnableTLS, _ = util.LookupBool(storeCfg, "tls")
```

- `NewAdapterFromConfig` 的 warmPool stateStore 块（`sc.KeyPrefix` 行后）加：

```go
			sc.EnableTLS, _ = util.LookupBool(storeCfg, "tls")
```

`configs/e2bgateway-example.yaml` stateStore 注释块（line ~86-93）改为：

```yaml
        stateStore:
          type: memory                        # memory | redis
          # Redis config (only when type: redis) — keeps idle sandbox records
          # across gateway restarts (pool definitions remain in-process):
          # addr: "redis:6379"                # or "rediss://redis:6379" to enable TLS implicitly
          # password: ""
          # tls: false                        # REQUIRED true (or rediss://) when password is set and addr is not loopback
          # db: 0
          # keyPrefix: "opensandbox:pool"
```

templateStore 的 redis 注释处（若有）加同样 `tls` 说明。

`warmpool_test.go` 的 `TestBuildPoolStateStore` 追加子测试：

```go
	t.Run("redis password without tls to remote addr is rejected", func(t *testing.T) {
		a := &Adapter{poolStoreCfg: &PoolStateStoreConfig{
			Type: "redis", Addr: "redis.example.com:6379", Password: "secret",
		}}
		_, err := a.buildPoolStateStore()
		assert.ErrorContains(t, err, "cleartext")
	})
	t.Run("redis password with tls to remote addr is accepted", func(t *testing.T) {
		a := &Adapter{poolStoreCfg: &PoolStateStoreConfig{
			Type: "redis", Addr: "redis.example.com:6379", Password: "secret", EnableTLS: true,
		}}
		store, err := a.buildPoolStateStore()
		require.NoError(t, err)
		assert.NotNil(t, store)
	})
	t.Run("rediss scheme enables tls implicitly", func(t *testing.T) {
		a := &Adapter{poolStoreCfg: &PoolStateStoreConfig{
			Type: "redis", Addr: "rediss://redis.example.com:6379", Password: "secret",
		}}
		store, err := a.buildPoolStateStore()
		require.NoError(t, err)
		assert.NotNil(t, store)
	})
```

（`redis.NewClient` 是懒连接，无服务器也能构造成功——既有子测试已依赖此行为。）

- [ ] **Step 6: 跑整个 opensandbox 包测试**

Run: `go test ./internal/adapter/opensandbox/ -race -v`
Expected: 全部 PASS（若 `redis_store_test.go` 用 miniredis：loopback 豁免保证不触发强制校验）。

- [ ] **Step 7: Commit**

```bash
git add internal/adapter/opensandbox/redis_tls.go internal/adapter/opensandbox/redis_tls_test.go internal/adapter/opensandbox/redis_store.go internal/adapter/opensandbox/warmpool.go internal/adapter/opensandbox/warmpool.go internal/adapter/opensandbox/factory.go internal/adapter/opensandbox/warmpool_test.go configs/e2bgateway-example.yaml
git commit -m "fix(adapter): require TLS for password-bearing remote Redis connections (opensandbox)"
```

---

### Task 4: A4 — idle 状态按镜像指纹隔离 namespace

**Files:**
- Modify: `internal/adapter/opensandbox/warmpool.go`（`buildPoolStateStore`、`defaultNewPool`）
- Test: `internal/adapter/opensandbox/warmpool_test.go`

**Interfaces:**
- Consumes: Task 3 后的 `buildPoolStateStore() (opensandbox.PoolStateStore, error)`。
- Produces:
  - `func poolKeyPrefix(configured, imageURI string) string` —— 生效前缀 = `configured`（空则 `"opensandbox:pool"`）+ `":img-"` + `sha256(imageURI)` 前 8 字节的 hex（16 字符）。
  - `buildPoolStateStore(imageURI string)` —— 签名变更，`defaultNewPool` 传入其已有的 `imageURI` 实参。

- [ ] **Step 1: 写失败测试**

`warmpool_test.go` 追加：

```go
func TestPoolKeyPrefix_ImageNamespaced(t *testing.T) {
	a := poolKeyPrefix("", "registry/base:v1")
	b := poolKeyPrefix("", "registry/base:v1")
	c := poolKeyPrefix("", "registry/base:v2")
	d := poolKeyPrefix("custom", "registry/base:v1")

	assert.Equal(t, a, b, "same image must yield a stable prefix (restart reuse)")
	assert.NotEqual(t, a, c, "different images must yield different namespaces")
	assert.True(t, strings.HasPrefix(a, "opensandbox:pool:img-"), "default prefix kept: %q", a)
	assert.True(t, strings.HasPrefix(d, "custom:img-"), "configured prefix kept: %q", d)
}
```

（`strings` 若未 import 则加。）

并把 `TestBuildPoolStateStore` 内所有 `a.buildPoolStateStore()` 调用改为 `a.buildPoolStateStore("registry/base:v1")`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/adapter/opensandbox/ -run "TestPoolKeyPrefix|TestBuildPoolStateStore" -v`
Expected: 编译失败（`poolKeyPrefix` 未定义、签名不匹配）。

- [ ] **Step 3: 实现**

`warmpool.go`：新增 import `crypto/sha256`。

```go
// poolKeyPrefix returns the effective Redis key prefix for a pool's persisted
// state: the configured prefix namespaced by an image fingerprint. The SDK
// keys idle-sandbox records by pool name only; without the image component a
// gateway restart that recreated the same pool ID with a changed
// template→image mapping could hand out sandboxes built from the OLD image
// while reporting the new pool's template (PR #82 review). Stale-namespace
// entries become unreachable and expire server-side at IdleTimeout.
func poolKeyPrefix(configured, imageURI string) string {
	if configured == "" {
		configured = "opensandbox:pool"
	}
	sum := sha256.Sum256([]byte(imageURI))
	return fmt.Sprintf("%s:img-%x", configured, sum[:8])
}
```

`buildPoolStateStore` 签名改为 `func (a *Adapter) buildPoolStateStore(imageURI string) (opensandbox.PoolStateStore, error)`；其中 keyPrefix 段改为：

```go
	keyPrefix := poolKeyPrefix(a.poolStoreCfg.KeyPrefix, imageURI)
```

（删除原先的 `if keyPrefix == "" { keyPrefix = "opensandbox:pool" }`。）

`defaultNewPool` 调用点改为 `a.buildPoolStateStore(imageURI)`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/adapter/opensandbox/ -race -v`
Expected: 全部 PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/opensandbox/warmpool.go internal/adapter/opensandbox/warmpool_test.go
git commit -m "fix(adapter): namespace persisted pool idle state by image fingerprint"
```

---

### Task 5: #80 — WarmPoolDeleteOptions 接口扩展 + opensandbox force 语义

**Files:**
- Modify: `internal/adapter/interface.go:81`（接口）+ `~line 320`（类型区）
- Modify: `internal/adapter/opensandbox/warmpool.go`（`DeleteWarmPool`）
- Modify: `internal/adapter/e2bcloud/adapter.go:515`、`internal/adapter/mock/adapter.go:510`
- Modify: `test/mock/backend.go:103,478-484`
- Modify: `internal/api/v1/ws_handler_test.go:130`（`stubAdapter`）
- Modify: `internal/api/v1/handlers.go:823`（临时传 `nil`，Task 7 换成解析后的 opts）
- Test: `internal/adapter/opensandbox/warmpool_test.go`

**Interfaces:**
- Consumes: opensandbox `fakeWarmPool`（Task 5 内扩展）、`newPoolTestAdapter`、`createTestPool`。
- Produces:
  - `type WarmPoolDeleteOptions struct { Force bool }`（`internal/adapter` 包）
  - 接口方法 `DeleteWarmPool(ctx context.Context, warmPoolID string, opts *WarmPoolDeleteOptions) error`；`opts == nil` ≡ graceful。
  - opensandbox：`Force` → `Shutdown(ctx, false)` + 仍执行 `ReleaseAllIdleParallel`。
  - `test/mock`：`DeleteWarmPoolFn func(ctx context.Context, id string, opts *adapter.WarmPoolDeleteOptions) error`。
  - Task 6 / Task 7 依赖本 task 的签名。

- [ ] **Step 1: 写失败测试（opensandbox force/graceful 双路径）**

`fakeWarmPool` 增加记录字段（struct 中 `shutdowns int` 旁）：

```go
	gracefulFlags []bool
```

其 `Shutdown` 方法在 `f.shutdowns++` 后加：

```go
	f.gracefulFlags = append(f.gracefulFlags, graceful)
```

并加读取 helper（测试断言用，避免直接锁操作）：

```go
func (f *fakeWarmPool) shutdownGracefulFlags() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.gracefulFlags...)
}
```

追加测试：

```go
func TestDeleteWarmPool_GracefulAndForce(t *testing.T) {
	t.Run("default is graceful shutdown plus drain", func(t *testing.T) {
		fake := &fakeWarmPool{}
		a := newPoolTestAdapter(fake)
		createTestPool(t, a, "wp-g")

		require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-g", nil))
		assert.Equal(t, []bool{true}, fake.shutdownGracefulFlags())
		assert.Equal(t, 1, fake.releases, "idle queue must be drained")
	})
	t.Run("force skips the in-flight drain", func(t *testing.T) {
		fake := &fakeWarmPool{}
		a := newPoolTestAdapter(fake)
		createTestPool(t, a, "wp-f")

		require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-f",
			&adapter.WarmPoolDeleteOptions{Force: true}))
		assert.Equal(t, []bool{false}, fake.shutdownGracefulFlags(),
			"force must call Shutdown(graceful=false)")
		assert.Equal(t, 1, fake.releases,
			"non-graceful Shutdown does not clear the idle queue; drain is still required")
	})
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/adapter/opensandbox/ -run TestDeleteWarmPool_GracefulAndForce -v`
Expected: 编译失败（`WarmPoolDeleteOptions` 未定义、`DeleteWarmPool` 参数不匹配）。

- [ ] **Step 3: 接口与类型**

`internal/adapter/interface.go` 的 `WarmPoolCreateRequest` 之后加：

```go
// WarmPoolDeleteOptions controls DeleteWarmPool teardown semantics. A nil
// *WarmPoolDeleteOptions is equivalent to &WarmPoolDeleteOptions{} (graceful).
type WarmPoolDeleteOptions struct {
	// Force requests immediate teardown instead of graceful:
	//   - opensandbox: Shutdown(graceful=false) — does not wait for in-flight
	//     work — followed by the same idle-queue drain.
	//   - agentsandbox: cascade-delete the CRD (Background propagation) and
	//     directly delete the pool's unclaimed sandboxes instead of waiting
	//     for controller reclaim. Claimed sandboxes are never touched.
	//   - e2bcloud / mock: currently no behavioral difference.
	Force bool
}
```

接口方法（line 81）改为：

```go
	DeleteWarmPool(ctx context.Context, warmPoolID string, opts *WarmPoolDeleteOptions) error
```

- [ ] **Step 4: 更新全部实现与调用点**

**opensandbox** `warmpool.go` `DeleteWarmPool`：签名加 `opts *adapter.WarmPoolDeleteOptions`；函数开头：

```go
	force := opts != nil && opts.Force
```

teardown 段改为（保留原 NOTE 注释，追加 force 说明）：

```go
	// Force teardown skips the graceful drain of in-flight work
	// (Shutdown graceful=false stops the reconciler immediately); the idle
	// queue is still released explicitly because neither shutdown mode
	// clears it.
	shutdownErr := entry.pool.Shutdown(ctx, !force)
```

`Shutdown` 调用前加一条日志（仅 force）：

```go
	if force {
		slog.Info("force-deleting warm pool", "adapter", a.name, "warmPoolID", warmPoolID)
	}
```

（`log/slog` 需加入 warmpool.go imports。）

**e2bcloud** `adapter.go:515`：

```go
func (a *Adapter) DeleteWarmPool(ctx context.Context, warmPoolID string, _ *adapter.WarmPoolDeleteOptions) error {
	return a.client.DeleteWarmPool(ctx, warmPoolID)
}
```

**mock** `adapter.go:510`：签名改为 `func (a *Adapter) DeleteWarmPool(_ context.Context, warmPoolID string, _ *adapter.WarmPoolDeleteOptions) error`，函数体不变。

**test/mock** `backend.go`：

```go
	DeleteWarmPoolFn     func(ctx context.Context, id string, opts *adapter.WarmPoolDeleteOptions) error
```

```go
func (m *MockBackend) DeleteWarmPool(ctx context.Context, id string, opts *adapter.WarmPoolDeleteOptions) error {
	m.record("DeleteWarmPool", ctx, id, opts)
	if m.DeleteWarmPoolFn != nil {
		return m.DeleteWarmPoolFn(ctx, id, opts)
	}
	return m.mock.DeleteWarmPool(ctx, id, opts)
}
```

**ws_handler_test.go:130** `stubAdapter`：

```go
func (s *stubAdapter) DeleteWarmPool(_ context.Context, _ string, _ *adapter.WarmPoolDeleteOptions) error {
	panic("not implemented")
}
```

**handlers.go:823**（临时，Task 7 替换）：

```go
			if err := a.DeleteWarmPool(r.Context(), warmPoolID, nil); err == nil {
```

**扫尾**：`grep -rn "\.DeleteWarmPool(" --include="*.go"` 找出剩余调用点（各 adapter 的 `*_test.go`、`test/e2e/`），统一补第三参 `nil`。然后：

Run: `go build ./... && go vet ./...`
Expected: 无编译错误。

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/adapter/... ./internal/api/... ./test/... -race`
Expected: 全部 PASS。

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(adapter): add WarmPoolDeleteOptions and opensandbox force delete semantics (#80)"
```

---

### Task 6: #80 — agentsandbox force 语义（Background propagation + 直删 unclaimed）

**Files:**
- Modify: `internal/adapter/agentsandbox/warmpool.go`（`DeleteWarmPool` + 新 helper）
- Test: `internal/adapter/agentsandbox/warmpool_test.go`

**Interfaces:**
- Consumes: Task 5 的 `adapter.WarmPoolDeleteOptions`；Task 1/2 后的 `DeleteWarmPool` 结构；`sandboxv1beta1.SandboxWarmPoolLabel`（= `agents.x-k8s.io/warm-pool-sandbox`）、`controllers.NameHash`（均已核实存在：`sigs.k8s.io/agent-sandbox/api/v1beta1/sandbox_types.go:167`、`sigs.k8s.io/agent-sandbox/controllers/sandbox_controller.go:1220`）。
- Produces: `forceDeleteUnclaimedPoolSandboxes(ctx, warmPoolID)`、`isClaimedSandbox(sb) bool`（包内私有）。

**已核实的关键事实**：claim controller 领养池中沙箱时会 `adopted.OwnerReferences = nil` + `SetControllerReference(claim, adopted, ...)`（`sandboxclaim_controller.go:1498-1499`）——claimed 沙箱的 controller ownerRef 指向 SandboxClaim，因此 (a) Background GC 删 WarmPool CRD 不会级联删 claimed 沙箱；(b) `GetControllerOf` 的 Kind 不是 `SandboxWarmPool` 即为"已被领养"，跳过。

- [ ] **Step 1: 写失败测试**

`warmpool_test.go` 新增 imports：

```go
	agentsfake "sigs.k8s.io/agent-sandbox/clients/k8s/clientset/versioned/fake"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	"sigs.k8s.io/agent-sandbox/controllers"
	"k8s.io/utils/ptr"
```

（`sandboxv1beta1` 若与既有 import 冲突则复用既有别名；`ptr` 已在 go.mod 依赖树中——k8s.io/utils。）

helper + 测试：

```go
// newWarmPoolForceTestAdapter extends newWarmPoolTestAdapter with a fake
// agents clientset so force-delete tests can seed pool Sandboxes.
func newWarmPoolForceTestAdapter(t *testing.T, extObjs []runtime.Object, sbObjs ...runtime.Object) (*Adapter, *extfake.Clientset, *agentsfake.Clientset) {
	t.Helper()
	a, ecs := newWarmPoolTestAdapter(t, extObjs...)
	acs := agentsfake.NewSimpleClientset(sbObjs...)
	a.k8s.AgentsClient = acs.AgentsV1beta1()
	return a, ecs, acs
}

// poolSandbox builds a Sandbox labeled as a member of poolID. controllerKind
// "" leaves it an orphan; "SandboxWarmPool" marks it pool-owned (unclaimed);
// "SandboxClaim" marks it claimed (adopted — the claim controller re-parents
// adopted sandboxes).
func poolSandbox(name, poolID, controllerKind string) *sandboxv1beta1.Sandbox {
	sb := &sandboxv1beta1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				sandboxv1beta1.SandboxWarmPoolLabel: controllers.NameHash(poolID),
			},
		},
	}
	if controllerKind != "" {
		sb.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: "agents.x-k8s.io/v1beta1",
			Kind:       controllerKind,
			Name:       poolID,
			UID:        "uid-1",
			Controller: ptr.To(true),
		}}
	}
	return sb
}

func TestDeleteWarmPool_ForceDeletesUnclaimedSandboxes(t *testing.T) {
	pool := warmPoolCRD("wp-1", "base", 3, time.Now())
	a, _, acs := newWarmPoolForceTestAdapter(t,
		[]runtime.Object{pool},
		poolSandbox("sb-pool", "wp-1", "SandboxWarmPool"),
		poolSandbox("sb-orphan", "wp-1", ""),
		poolSandbox("sb-claimed", "wp-1", "SandboxClaim"),
	)
	a.warmPoolMap["base"] = "wp-1"

	require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-1",
		&adapter.WarmPoolDeleteOptions{Force: true}))

	sandboxes := acs.AgentsV1beta1().Sandboxes("default")
	_, err := sandboxes.Get(context.Background(), "sb-pool", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "pool-owned unclaimed sandbox must be deleted")
	_, err = sandboxes.Get(context.Background(), "sb-orphan", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "orphaned unclaimed sandbox must be deleted")
	_, err = sandboxes.Get(context.Background(), "sb-claimed", metav1.GetOptions{})
	assert.NoError(t, err, "claimed sandbox must never be touched")
}

func TestDeleteWarmPool_GracefulDoesNotDeleteSandboxesDirectly(t *testing.T) {
	pool := warmPoolCRD("wp-1", "base", 3, time.Now())
	a, _, acs := newWarmPoolForceTestAdapter(t,
		[]runtime.Object{pool},
		poolSandbox("sb-pool", "wp-1", "SandboxWarmPool"),
	)

	require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-1", nil))

	_, err := acs.AgentsV1beta1().Sandboxes("default").
		Get(context.Background(), "sb-pool", metav1.GetOptions{})
	assert.NoError(t, err, "graceful delete leaves sandbox reclaim to the controller/GC")
}

func TestDeleteWarmPool_ForceUsesBackgroundPropagation(t *testing.T) {
	pool := warmPoolCRD("wp-1", "base", 1, time.Now())
	a, ecs, acs := newWarmPoolForceTestAdapter(t, []runtime.Object{pool})
	// agents fake 无沙箱也需要 AgentsClient 非 nil：
	_ = acs

	require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-1",
		&adapter.WarmPoolDeleteOptions{Force: true}))

	for _, action := range ecs.Actions() {
		if action.GetVerb() == "delete" && action.GetResource().Resource == "sandboxwarmpools" {
			da := action.(k8stesting.DeleteActionImpl)
			require.NotNil(t, da.DeleteOptions.PropagationPolicy)
			assert.Equal(t, metav1.DeletePropagationBackground, *da.DeleteOptions.PropagationPolicy)
			return
		}
	}
	t.Fatal("no sandboxwarmpools delete action recorded")
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/adapter/agentsandbox/ -run "TestDeleteWarmPool_Force|TestDeleteWarmPool_GracefulDoesNot" -v`
Expected: FAIL（force 无效果 / 无 propagation）。

- [ ] **Step 3: 实现**

`warmpool.go` 新增 import：`"sigs.k8s.io/agent-sandbox/controllers"`（`sandboxv1beta1` 已随 adapter.go 在包内使用——warmpool.go 自身需要则加同名 import；`apierrors`、`slog` 已在）。

`DeleteWarmPool` 签名加 `opts *adapter.WarmPoolDeleteOptions`，函数体开头：

```go
	force := opts != nil && opts.Force
	deleteOpts := metav1.DeleteOptions{}
	if force {
		// Cascade-delete the pool-owned (unclaimed) sandboxes together with
		// the CRD instead of waiting for controller reclaim. Claimed
		// sandboxes are immune: the claim controller re-parents adopted
		// sandboxes to the SandboxClaim, so they are no longer owned by the
		// pool. Graceful deletes keep the server-default propagation to
		// preserve the pre-force behavior (controller reclaims per
		// updateStrategy).
		deleteOpts.PropagationPolicy = ptr.To(metav1.DeletePropagationBackground)
	}
```

（import `"k8s.io/utils/ptr"`。）

`Delete` 调用改用 `deleteOpts`；成功后、清缓存路由（Task 1 代码）之前插入：

```go
	if force {
		a.forceDeleteUnclaimedPoolSandboxes(ctx, warmPoolID)
	}
```

文件末尾新增：

```go
// forceDeleteUnclaimedPoolSandboxes directly deletes the pool's unclaimed
// sandboxes so a force delete does not wait for garbage collection. The
// warm-pool label value is the controller's NameHash of the pool name (see
// the agent-sandbox warmpool controller). Best-effort: failures are logged
// and left to Background propagation GC; they do not fail the delete.
func (a *Adapter) forceDeleteUnclaimedPoolSandboxes(ctx context.Context, warmPoolID string) {
	if a.k8s.AgentsClient == nil {
		return
	}
	selector := sandboxv1beta1.SandboxWarmPoolLabel + "=" + controllers.NameHash(warmPoolID)
	list, err := a.k8s.AgentsClient.Sandboxes(a.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		slog.Warn("force delete: listing pool sandboxes failed; background GC will reclaim them",
			"adapter", a.name, "warmPoolID", warmPoolID, "error", err)
		return
	}
	for i := range list.Items {
		sb := &list.Items[i]
		if isClaimedSandbox(sb) {
			continue
		}
		slog.Info("force-deleting unclaimed pool sandbox",
			"adapter", a.name, "warmPoolID", warmPoolID, "sandbox", sb.Name)
		if err := a.k8s.AgentsClient.Sandboxes(a.namespace).Delete(ctx, sb.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			slog.Warn("force delete: deleting pool sandbox failed; background GC will reclaim it",
				"adapter", a.name, "warmPoolID", warmPoolID, "sandbox", sb.Name, "error", err)
		}
	}
}

// isClaimedSandbox reports whether a pool-labeled sandbox has been adopted by
// a SandboxClaim (or any other controller). Adoption re-parents the sandbox
// (OwnerReferences reset + controller ref set to the claim), so pool-owned
// and orphaned sandboxes are the only safe force-delete targets.
func isClaimedSandbox(sb *sandboxv1beta1.Sandbox) bool {
	ref := metav1.GetControllerOfNoCopy(sb)
	return ref != nil && ref.Kind != "SandboxWarmPool"
}
```

- [ ] **Step 4: 跑测试确认通过（含回归）**

Run: `go test ./internal/adapter/agentsandbox/ -race -v`
Expected: 全部 PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/agentsandbox/warmpool.go internal/adapter/agentsandbox/warmpool_test.go
git commit -m "feat(adapter): agentsandbox force warm pool delete removes unclaimed sandboxes (#80)"
```

---

### Task 7: #80 — HTTP handler force param + OpenAPI

**Files:**
- Modify: `internal/api/v1/handlers.go:817-831`（`DeleteWarmPoolHandler`）
- Modify: `docs/api/openapi.yaml:909-933`（`delete` 段）
- Test: `internal/api/v1/handlers_test.go`

**Interfaces:**
- Consumes: Task 5 的 `adapter.WarmPoolDeleteOptions`、`test/mock.MockBackend`（记录 opts）。
- Produces: `?force=<bool>` → `opts.Force`；非法值 → 400；缺省 → `opts == nil`。

- [ ] **Step 1: 写失败测试**

`handlers_test.go` 追加（imports 视需要补 `"github.com/e2bgateway/e2bgateway/test/mock"` 别名 `testmock`、`adapter`；`setupTestRouter` 用的是 `mockadapter`，这里单独构造带 `MockBackend` 的 registry）：

```go
func TestDeleteWarmPoolHandler_ForceParam(t *testing.T) {
	newRegWithMock := func() (*adapter.Registry, *routing.Router, *testmock.MockBackend) {
		reg := adapter.NewRegistry()
		mb := testmock.NewMockBackend()
		_ = reg.Register(mb)
		r := routing.NewRouter(config.RoutingConfig{DefaultBackend: "mock", Strategy: "static"}, reg)
		return reg, r, mb
	}

	t.Run("force=true reaches the adapter", func(t *testing.T) {
		reg, r, mb := newRegWithMock()
		handler := v1.DeleteWarmPoolHandler(reg, r)
		req := testWithChiParam(http.MethodDelete, "/api/v1/warm-pools/wp-1?force=true", "",
			map[string]string{"warmPoolID": "wp-1"})
		w := httptest.NewRecorder()
		handler(w, req)

		require.Equal(t, http.StatusNoContent, w.Code)
		var got *adapter.WarmPoolDeleteOptions
		for _, c := range mb.CallsSnapshot() {
			if c.Method == "DeleteWarmPool" && len(c.Args) == 3 {
				got, _ = c.Args[2].(*adapter.WarmPoolDeleteOptions)
			}
		}
		require.NotNil(t, got, "opts must be passed through")
		assert.True(t, got.Force)
	})

	t.Run("no param means nil opts (graceful)", func(t *testing.T) {
		reg, r, mb := newRegWithMock()
		handler := v1.DeleteWarmPoolHandler(reg, r)
		req := testWithChiParam(http.MethodDelete, "/api/v1/warm-pools/wp-1", "",
			map[string]string{"warmPoolID": "wp-1"})
		w := httptest.NewRecorder()
		handler(w, req)

		require.Equal(t, http.StatusNoContent, w.Code)
		found := false
		for _, c := range mb.CallsSnapshot() {
			if c.Method == "DeleteWarmPool" {
				found = true
				assert.Nil(t, c.Args[2])
			}
		}
		assert.True(t, found)
	})

	t.Run("invalid force value is 400", func(t *testing.T) {
		reg, r, _ := newRegWithMock()
		handler := v1.DeleteWarmPoolHandler(reg, r)
		req := testWithChiParam(http.MethodDelete, "/api/v1/warm-pools/wp-1?force=garbage", "",
			map[string]string{"warmPoolID": "wp-1"})
		w := httptest.NewRecorder()
		handler(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}
```

注意：`testWithChiParam` 用 `httptest.NewRequest(method, path, ...)`，query string 会保留在 `req.URL` 中，handler 可正常读取。`MethodCall` 的字段名（`Method`/`Args`）以 `test/mock/backend.go` 实际定义为准，写测试前先核对；`testmock.NewMockBackend()` 的构造函数名同样以实际为准（若无导出构造函数，用 `&testmock.MockBackend{...}` 按文件内既有方式构造）。`MockBackend` 需要能注册进 `adapter.Registry`（它实现 `SandboxAdapter`——`backend.go` 已保证）。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api/v1/ -run TestDeleteWarmPoolHandler_ForceParam -v`
Expected: FAIL（`force=garbage` 当前返回 204/404 而非 400；opts 恒为 nil）。

- [ ] **Step 3: 实现 handler**

`handlers.go` `DeleteWarmPoolHandler` 改为（`strconv` 加入 imports，若未有）：

```go
// DeleteWarmPoolHandler handles DELETE /api/v1/warm-pools/{warmPoolID}.
// An optional ?force=true requests immediate teardown semantics (see
// adapter.WarmPoolDeleteOptions); an unparsable value is a 400.
func DeleteWarmPoolHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		warmPoolID := chi.URLParam(r, "warmPoolID")

		var opts *adapter.WarmPoolDeleteOptions
		if raw := r.URL.Query().Get("force"); raw != "" {
			force, err := strconv.ParseBool(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "Invalid force parameter: "+raw)
				return
			}
			opts = &adapter.WarmPoolDeleteOptions{Force: force}
		}

		for _, a := range registry.List() {
			if err := a.DeleteWarmPool(r.Context(), warmPoolID, opts); err == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Warm pool '"+warmPoolID+"' not found")
	}
}
```

- [ ] **Step 4: OpenAPI**

`docs/api/openapi.yaml` 的 `/warm-pools/{warmPoolID}` `delete:` 段，在 `summary` 与 `responses` 之间加 `parameters`，并在 responses 加 400：

```yaml
    delete:
      tags: [Warm Pools]
      operationId: deleteWarmPool
      summary: Delete a warm pool
      parameters:
        - name: force
          in: query
          required: false
          description: >-
            Force immediate teardown. opensandbox: non-graceful shutdown (does
            not wait for in-flight work) plus idle-queue drain. agentsandbox:
            cascade-delete the pool CRD and directly delete its unclaimed
            sandboxes instead of waiting for controller reclaim. Claimed
            sandboxes are never affected. Defaults to false (graceful).
          schema:
            type: boolean
            default: false
      responses:
        "204":
          description: Warm pool deleted
        "400":
          description: Invalid force parameter
        "404":
          $ref: "#/components/responses/NotFound"
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/api/... -race -v -run "WarmPool|DeleteWarmPool"`
Expected: 全部 PASS（含既有 `TestWarmPoolLifecycle`）。

- [ ] **Step 6: Commit**

```bash
git add internal/api/v1/handlers.go internal/api/v1/handlers_test.go docs/api/openapi.yaml
git commit -m "feat(api): support ?force=true on warm pool delete (#80)"
```

---

### Task 8: 收尾验证

**Files:**
- 无新改动（验证 + 可能的 lint 修补）

- [ ] **Step 1: 全量 CI 门槛**

Run: `make pre-commit`
Expected: fmt / vet / lint / test 全绿。lint 报的问题就地修复后重跑。

- [ ] **Step 2: 全仓 grep 确认无遗漏调用点**

Run: `grep -rn "DeleteWarmPool(" --include="*.go" . | grep -v "opts\|_ \*adapter.WarmPoolDeleteOptions\|nil)"`
Expected: 仅剩接口定义与文档性引用，无旧签名残留。

- [ ] **Step 3: 对照 spec 验收清单**

- Issue #80 验收标准逐条核对：两 adapter force 语义 ✅（Task 5/6）、handler 解析 ✅（Task 7）、OpenAPI ✅（Task 7）、graceful/force 单测 ✅。
- 4 条 coderabbit 评论逐条核对：A1（Task 2）、A2（Task 1）、A3（Task 3）、A4（Task 4）。

- [ ] **Step 4: 若有修补则 commit**

```bash
git add -A
git commit -m "chore: address lint findings from pre-commit"
```

---

## Checklist

- [ ] Spec reviewed（已完成，2026-11-13，Q1 已核实并回写 spec）
- [ ] All adapters implement new interface methods (incl. mock, e2bcloud, test/mock, ws_handler_test stubAdapter)
- [ ] Unit tests pass with `-race`
- [ ] `make pre-commit` 全绿
- [ ] Config example updated（`configs/e2bgateway-example.yaml` stateStore `tls` 说明）
- [ ] OpenAPI docs updated（`force` query param + 400）
- [ ] `golangci-lint` clean（无新 gocyclo 告警）
- [ ] PR 描述链接 spec，并按 `submit-pr` skill 规范提交；PR 中逐条回复 4 条 coderabbit 评论
