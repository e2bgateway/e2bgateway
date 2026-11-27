# 两级缓存（L1 内存 + L2 Redis + 跨副本失效）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现 issue #113：`TieredCache[T]` 组合 L1 内存 LRU 与 L2 Redis，Pub/Sub 跨副本失效，接入 sandbox metadata / template list / readyz 热点路径，L2 故障自动降级。

**Architecture:** 泛型组合器放 `internal/cache/`，handler 层通过 `XxxHandlerWithCache` 变体显式接入（nil = 直通，原构造函数保留为一行委托，现有 ~60 处测试调用点不动）。`server.New` 按 `cache.tiered` 配置构造缓存与 Pub/Sub 订阅。不改 `SandboxAdapter` 接口。

**Tech Stack:** Go 1.26+、现有 `internal/cache`（LRU / RedisCache）、go-redis v9、`golang.org/x/sync/singleflight`（已在 go.mod，转直接依赖）、miniredis v2、testify、Prometheus client_golang。

**Spec:** `docs/superpowers/specs/2026-11-27-tiered-cache-design.md`

## Global Constraints

- 默认关闭：`cache.tiered.enabled` 默认 `false`；关闭时所有路径行为与现状逐字节一致。
- 不改 `SandboxAdapter` 接口；四个 adapter（agentsandbox/opensandbox/e2bcloud/mock）零改动。
- 任何 L2 错误不得使请求失败；单次 L2 操作超时默认 `200ms`。
- 缓存值不含凭证（`dto.SandboxInfo` / `adapter.Template` 已核实无 token 字段）。
- 新文件带 Apache-2.0 license header（照抄 `internal/cache/cache.go:1-13`）。
- 测试：testify `assert`/`require`、表驱动优先、全部跑 `-race`。
- 提交：conventional commits + `git commit -s`（DCO，仓库硬性要求）。
- Viper 小写化：`mapstructure` 标签大小写不敏感，配置测试覆盖 camelCase 与 lowercase 两种写法。
- 日志：`slog` 结构化；L2 降级 WARN 按缓存实例节流（10s 一条）。
- 每个 Task 结束跑 `go test -race ./<受影响包>/...` 并提交；最终 `make pre-commit`。

---

### Task 1: 配置 — `cache.tiered` 子树

**Files:**
- Modify: `internal/config/config.go`（`CacheConfig` 约 :158；`setDefaults` 约 :295；`Validate` 约 :313）
- Test: `internal/config/tiered_cache_test.go`（新建）

**Interfaces:**
- Consumes: 现有 `RedisConfig{Address,Password,DB}`（config.go:115）、`redisx.NormalizeRedisAddr` / `redisx.AddrRequiresTLS`（`internal/redisx/redisx.go:30/53`）
- Produces: `config.TieredCacheConfig{Enabled bool; L1 TieredL1Config; L2 TieredL2Config}`、`config.TieredL1Config{MaxSize int; DefaultTTL time.Duration}`、`config.TieredL2Config{RedisConfig(squash); TLS bool; KeyPrefix string; PubsubChannel string; Timeout time.Duration}`、`(*TieredCacheConfig).validate() error`。后续 Task 8 读取这些字段。

- [ ] **Step 1: 写失败测试**

`internal/config/tiered_cache_test.go`（package config，带 license header）：

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const tieredTestBase = `
server:
  http:
    address: ":8080"
backends:
  - name: mock
    type: mock
    enabled: true
`

func writeTieredConfig(t *testing.T, extra string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cfg.yaml")
	require.NoError(t, os.WriteFile(path, []byte(tieredTestBase+extra), 0o600))
	return path
}

func TestTieredCacheDefaults(t *testing.T) {
	cfg, err := Load(writeTieredConfig(t, ""))
	require.NoError(t, err)
	assert.False(t, cfg.Cache.Tiered.Enabled)
	assert.Equal(t, 10000, cfg.Cache.Tiered.L1.MaxSize)
	assert.Equal(t, 30*time.Second, cfg.Cache.Tiered.L1.DefaultTTL)
	assert.Equal(t, "e2bgw:cache:", cfg.Cache.Tiered.L2.KeyPrefix)
	assert.Equal(t, "e2bgw:cache:invalidate", cfg.Cache.Tiered.L2.PubsubChannel)
	assert.Equal(t, 200*time.Millisecond, cfg.Cache.Tiered.L2.Timeout)
}

func TestTieredCacheParseCamelAndLowercase(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{"camelCase", `
cache:
  tiered:
    enabled: true
    l1:
      maxSize: 500
      defaultTTL: 45s
    l2:
      address: "127.0.0.1:6379"
      pubsubChannel: "test:invalidate"
      timeout: 150ms
`},
		{"lowercase", `
cache:
  tiered:
    enabled: true
    l1:
      maxsize: 500
      defaultttl: 45s
    l2:
      address: "127.0.0.1:6379"
      pubsubchannel: "test:invalidate"
      timeout: 150ms
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeTieredConfig(t, tc.yaml))
			require.NoError(t, err)
			assert.True(t, cfg.Cache.Tiered.Enabled)
			assert.Equal(t, 500, cfg.Cache.Tiered.L1.MaxSize)
			assert.Equal(t, 45*time.Second, cfg.Cache.Tiered.L1.DefaultTTL)
			assert.Equal(t, "127.0.0.1:6379", cfg.Cache.Tiered.L2.Address)
			assert.Equal(t, "test:invalidate", cfg.Cache.Tiered.L2.PubsubChannel)
			assert.Equal(t, 150*time.Millisecond, cfg.Cache.Tiered.L2.Timeout)
		})
	}
}

func TestTieredCacheValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*TieredCacheConfig)
		wantErr bool
	}{
		{"disabled skips validation", func(c *TieredCacheConfig) { c.Enabled = false; c.L1.MaxSize = 0 }, false},
		{"valid L1 only", func(c *TieredCacheConfig) {}, false},
		{"bad maxSize", func(c *TieredCacheConfig) { c.L1.MaxSize = 0 }, true},
		{"bad timeout", func(c *TieredCacheConfig) { c.L2.Timeout = 0 }, true},
		{"password plaintext non-loopback", func(c *TieredCacheConfig) {
			c.L2.Address = "redis.prod:6379"
			c.L2.Password = "secret"
		}, true},
		{"password with TLS ok", func(c *TieredCacheConfig) {
			c.L2.Address = "redis.prod:6379"
			c.L2.Password = "secret"
			c.L2.TLS = true
		}, false},
		{"password plaintext loopback ok", func(c *TieredCacheConfig) {
			c.L2.Address = "127.0.0.1:6379"
			c.L2.Password = "secret"
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := TieredCacheConfig{
				Enabled: true,
				L1:      TieredL1Config{MaxSize: 10, DefaultTTL: time.Second},
				L2:      TieredL2Config{Timeout: 100 * time.Millisecond},
			}
			tc.mutate(&c)
			err := c.validate()
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
```

- [ ] **Step 2: 跑测试确认编译失败**

Run: `go test ./internal/config/ -run TestTiered -count=1`
Expected: FAIL（`TieredCacheConfig` 未定义）

- [ ] **Step 3: 实现**

`internal/config/config.go`：

(a) import 块加 `"github.com/e2bgateway/e2bgateway/internal/redisx"`。

(b) `CacheConfig` 追加字段：

```go
type CacheConfig struct {
	Backend string            `mapstructure:"backend"` // memory, redis
	Memory  MemoryConfig      `mapstructure:"memory"`
	Redis   RedisConfig       `mapstructure:"redis"`
	Tiered  TieredCacheConfig `mapstructure:"tiered"`
}
```

(c) 新增类型（放 `MemoryConfig` 之后）：

```go
// TieredCacheConfig configures the L1 (in-memory) + L2 (Redis) tiered cache
// with cross-replica invalidation (issue #113, design §15.2). Disabled by
// default; enabled without l2.address runs L1-only.
type TieredCacheConfig struct {
	Enabled bool           `mapstructure:"enabled"`
	L1      TieredL1Config `mapstructure:"l1"`
	L2      TieredL2Config `mapstructure:"l2"`
}

// TieredL1Config defines the per-pod in-memory tier.
type TieredL1Config struct {
	MaxSize    int           `mapstructure:"maxSize"`
	DefaultTTL time.Duration `mapstructure:"defaultTTL"`
}

// TieredL2Config defines the shared Redis tier and invalidation Pub/Sub.
type TieredL2Config struct {
	RedisConfig   `mapstructure:",squash"`
	TLS           bool          `mapstructure:"tls"`
	KeyPrefix     string        `mapstructure:"keyPrefix"`
	PubsubChannel string        `mapstructure:"pubsubChannel"`
	Timeout       time.Duration `mapstructure:"timeout"`
}

// validate rejects unusable tiered-cache configurations. Cached payloads
// carry no credentials, so non-loopback plaintext is a startup WARN (handled
// in server wiring), but sending a password in cleartext is rejected.
func (c *TieredCacheConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if c.L1.MaxSize <= 0 {
		return fmt.Errorf("cache.tiered.l1.maxSize must be > 0")
	}
	if c.L1.DefaultTTL <= 0 {
		return fmt.Errorf("cache.tiered.l1.defaultTTL must be > 0")
	}
	if c.L2.Timeout <= 0 {
		return fmt.Errorf("cache.tiered.l2.timeout must be > 0")
	}
	if c.L2.Address != "" {
		addr, schemeTLS := redisx.NormalizeRedisAddr(c.L2.Address)
		if c.L2.Password != "" && !c.L2.TLS && !schemeTLS && redisx.AddrRequiresTLS(addr) {
			return fmt.Errorf("cache.tiered.l2: password would be sent to non-loopback redis %q without TLS; set tls: true or use a rediss:// address", c.L2.Address)
		}
	}
	return nil
}
```

(d) `setDefaults` 追加（现有 `cache.*` 默认值之后）：

```go
	v.SetDefault("cache.tiered.enabled", false)
	v.SetDefault("cache.tiered.l1.maxSize", 10000)
	v.SetDefault("cache.tiered.l1.defaultTTL", "30s")
	v.SetDefault("cache.tiered.l2.keyPrefix", "e2bgw:cache:")
	v.SetDefault("cache.tiered.l2.pubsubChannel", "e2bgw:cache:invalidate")
	v.SetDefault("cache.tiered.l2.timeout", "200ms")
```

(e) `Config.Validate` 中 `c.SessionStore.validate()` 调用之后追加：

```go
	if err := c.Cache.Tiered.validate(); err != nil {
		return err
	}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test -race ./internal/config/ -count=1`
Expected: PASS（含既有用例）

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/tiered_cache_test.go
git commit -s -m "feat(config): add cache.tiered configuration subtree (#113)"
```

---

### Task 2: Metrics — 缓存命中/失效计数器

**Files:**
- Modify: `internal/metrics/metrics.go`（`Metrics` struct 字段区、`Init` :78、recorder 区 :205 附近）
- Test: `internal/metrics/cache_test.go`（新建）

**Interfaces:**
- Consumes: 现有 `Init(cfg config.MetricsConfig, src Sources)`、`Current()`、`Reset()`
- Produces: `metrics.RecordCache(name, event string)`，event ∈ `"l1_hit"`, `"l2_hit"`, `"miss"`, `"invalidation"`, `"l2_error"`；counters `e2bgw_cache_l1_hits_total` / `l2_hits_total` / `misses_total` / `invalidations_total` / `l2_errors_total`，统一标签 `cache`。Task 3/4 调用 `RecordCache`。

- [ ] **Step 1: 写失败测试**

`internal/metrics/cache_test.go`（package metrics，license header）：

```go
package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"

	"github.com/e2bgateway/e2bgateway/internal/config"
)

func TestRecordCache(t *testing.T) {
	Init(config.MetricsConfig{Enabled: true}, Sources{})
	t.Cleanup(Reset)

	RecordCache("sandbox_metadata", "l1_hit")
	RecordCache("sandbox_metadata", "l1_hit")
	RecordCache("sandbox_metadata", "l2_hit")
	RecordCache("template_list", "miss")
	RecordCache("template_list", "invalidation")
	RecordCache("template_list", "l2_error")
	RecordCache("template_list", "unknown_event") // must not panic

	m := Current()
	assert.Equal(t, 2.0, testutil.ToFloat64(m.cacheL1Hits.WithLabelValues("sandbox_metadata")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.cacheL2Hits.WithLabelValues("sandbox_metadata")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.cacheMisses.WithLabelValues("template_list")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.cacheInvalidations.WithLabelValues("template_list")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.cacheL2Errors.WithLabelValues("template_list")))
}

func TestRecordCacheNoopBeforeInit(t *testing.T) {
	Reset()
	assert.NotPanics(t, func() { RecordCache("sandbox_metadata", "miss") })
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/metrics/ -run TestRecordCache -count=1`
Expected: FAIL（`RecordCache` / 字段未定义）

- [ ] **Step 3: 实现**

(a) `Metrics` struct 追加字段（`warmPoolAcquire` 附近）：

```go
	cacheL1Hits        *prometheus.CounterVec
	cacheL2Hits        *prometheus.CounterVec
	cacheMisses        *prometheus.CounterVec
	cacheInvalidations *prometheus.CounterVec
	cacheL2Errors      *prometheus.CounterVec
```

(b) `Init` 中（`m.tlsReloads` 赋值之后）：

```go
	m.cacheL1Hits = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "cache_l1_hits_total",
		Help: "Tiered cache L1 (in-memory) hits by cache instance.",
	}, []string{"cache"})
	m.cacheL2Hits = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "cache_l2_hits_total",
		Help: "Tiered cache L2 (Redis) hits by cache instance.",
	}, []string{"cache"})
	m.cacheMisses = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "cache_misses_total",
		Help: "Tiered cache misses that triggered a backend load, by cache instance.",
	}, []string{"cache"})
	m.cacheInvalidations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "cache_invalidations_total",
		Help: "Remote invalidation broadcasts applied to L1, by cache instance.",
	}, []string{"cache"})
	m.cacheL2Errors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "cache_l2_errors_total",
		Help: "L2 (Redis) operation failures causing degradation, by cache instance.",
	}, []string{"cache"})
```

并把这 5 个 collector 加进 `m.reg.MustRegister(...)` 参数列表。

(c) recorder（`RecordLifecycle` 之后）：

```go
// RecordCache records one tiered-cache event. name is the cache instance
// label (sandbox_metadata, template_list); event is one of l1_hit, l2_hit,
// miss, invalidation, l2_error. Unknown events are ignored.
func RecordCache(name, event string) {
	m := Current()
	if m == nil {
		return
	}
	switch event {
	case "l1_hit":
		m.cacheL1Hits.WithLabelValues(name).Inc()
	case "l2_hit":
		m.cacheL2Hits.WithLabelValues(name).Inc()
	case "miss":
		m.cacheMisses.WithLabelValues(name).Inc()
	case "invalidation":
		m.cacheInvalidations.WithLabelValues(name).Inc()
	case "l2_error":
		m.cacheL2Errors.WithLabelValues(name).Inc()
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test -race ./internal/metrics/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/metrics/metrics.go internal/metrics/cache_test.go
git commit -s -m "feat(metrics): tiered cache hit/miss/invalidation counters (#113)"
```

---

### Task 3: `TieredCache[T]` 核心 + go-redis 适配器

**Files:**
- Create: `internal/cache/tiered.go`
- Create: `internal/cache/redis_client.go`
- Test: `internal/cache/tiered_test.go`（新建）
- Modify: `go.mod`（`go mod tidy` 把 `golang.org/x/sync` 转直接依赖）

**Interfaces:**
- Consumes: 现有 `*Cache`（`New/Get/SetWithTTL/Delete`）、`*RedisCache`（`NewRedisCache/Get/SetWithTTL/Delete`）、`RedisCacheClient` 接口（redis.go:24）、`metrics.RecordCache`（Task 2）
- Produces（Task 4/5/6/7/8 依赖，签名必须一致）:
  - 常量 `cache.SandboxMetadataTTL = 30*time.Second`、`cache.TemplateListTTL = 60*time.Second`、`cache.BackendHealthTTL = 10*time.Second`
  - `type Broadcaster interface { Broadcast(ctx context.Context, cacheName, key string) }`
  - `type TieredOptions struct { Name string; L1MaxSize int; L1TTL time.Duration; L2 *RedisCache; Timeout time.Duration; Invalidator Broadcaster }`
  - `func NewTiered[T any](opts TieredOptions) *TieredCache[T]`
  - `func (tc *TieredCache[T]) GetOrLoad(ctx context.Context, key string, ttl time.Duration, load func(context.Context) (T, error)) (T, error)`
  - `func (tc *TieredCache[T]) Set(ctx context.Context, key string, val T, ttl time.Duration)`
  - `func (tc *TieredCache[T]) Invalidate(ctx context.Context, key string)`
  - `func (tc *TieredCache[T]) OnRemoteInvalidate(key string)`
  - `func WrapRedisClient(client redis.UniversalClient) RedisCacheClient`

- [ ] **Step 1: 写失败测试**

`internal/cache/tiered_test.go`（package cache，license header）：

```go
package cache

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTieredForTest[T any](t *testing.T, mr *miniredis.Miniredis, name string) *TieredCache[T] {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewTiered[T](TieredOptions{
		Name: name, L1MaxSize: 100, L1TTL: time.Minute,
		L2: NewRedisCache(WrapRedisClient(client), "test:", time.Minute),
	})
}

func strLoader(v string, calls *atomic.Int64) func(context.Context) (string, error) {
	return func(context.Context) (string, error) {
		if calls != nil {
			calls.Add(1)
		}
		return v, nil
	}
}

func TestTiered_L1Hit(t *testing.T) {
	mr := miniredis.RunT(t)
	tc := newTieredForTest[string](t, mr, "test")
	var calls atomic.Int64
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		v, err := tc.GetOrLoad(ctx, "k", time.Minute, strLoader("v1", &calls))
		require.NoError(t, err)
		assert.Equal(t, "v1", v)
	}
	assert.Equal(t, int64(1), calls.Load())
}

func TestTiered_L2HitBackfillsL1(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newTieredForTest[string](t, mr, "test")
	b := newTieredForTest[string](t, mr, "test") // fresh L1, shared L2
	ctx := context.Background()

	_, err := a.GetOrLoad(ctx, "k", time.Minute, strLoader("v1", nil))
	require.NoError(t, err)

	var calls atomic.Int64
	v, err := b.GetOrLoad(ctx, "k", time.Minute, strLoader("SHOULD-NOT-LOAD", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v1", v)
	assert.Equal(t, int64(0), calls.Load())
	// backfilled into b's L1: delete L2, b still hits L1
	mr.FlushAll()
	v, err = b.GetOrLoad(ctx, "k", time.Minute, strLoader("SHOULD-NOT-LOAD", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v1", v)
}

func TestTiered_MissFillsBothTiers(t *testing.T) {
	mr := miniredis.RunT(t)
	tc := newTieredForTest[string](t, mr, "test")
	ctx := context.Background()

	_, err := tc.GetOrLoad(ctx, "k", time.Minute, strLoader("v1", nil))
	require.NoError(t, err)

	raw, hit, err := tc.l2.Get(ctx, "k")
	require.NoError(t, err)
	require.True(t, hit)
	assert.JSONEq(t, `"v1"`, raw)
	_, ok := tc.l1.Get("k")
	assert.True(t, ok)
}

func TestTiered_SetWriteThrough(t *testing.T) {
	mr := miniredis.RunT(t)
	tc := newTieredForTest[string](t, mr, "test")
	ctx := context.Background()

	tc.Set(ctx, "k", "v2", time.Minute)

	raw, hit, err := tc.l2.Get(ctx, "k")
	require.NoError(t, err)
	assert.True(t, hit)
	assert.JSONEq(t, `"v2"`, raw)
	var calls atomic.Int64
	v, err := tc.GetOrLoad(ctx, "k", time.Minute, strLoader("NO", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v2", v)
	assert.Equal(t, int64(0), calls.Load())
}

func TestTiered_Invalidate(t *testing.T) {
	mr := miniredis.RunT(t)
	tc := newTieredForTest[string](t, mr, "test")
	ctx := context.Background()

	_, err := tc.GetOrLoad(ctx, "k", time.Minute, strLoader("v1", nil))
	require.NoError(t, err)
	tc.Invalidate(ctx, "k")

	_, ok := tc.l1.Get("k")
	assert.False(t, ok)
	_, hit, err := tc.l2.Get(ctx, "k")
	require.NoError(t, err)
	assert.False(t, hit)
}

func TestTiered_TTLExpiry(t *testing.T) {
	mr := miniredis.RunT(t)
	tc := newTieredForTest[string](t, mr, "test")
	var calls atomic.Int64
	ctx := context.Background()

	_, err := tc.GetOrLoad(ctx, "k", 20*time.Millisecond, strLoader("v1", &calls))
	require.NoError(t, err)
	time.Sleep(40 * time.Millisecond)
	mr.FastForward(40 * time.Millisecond)
	_, err = tc.GetOrLoad(ctx, "k", 20*time.Millisecond, strLoader("v1", &calls))
	require.NoError(t, err)
	assert.Equal(t, int64(2), calls.Load())
}

func TestTiered_L2DownDegrades(t *testing.T) {
	mr := miniredis.RunT(t)
	tc := newTieredForTest[string](t, mr, "test")
	ctx := context.Background()
	_, err := tc.GetOrLoad(ctx, "k", time.Minute, strLoader("v1", nil))
	require.NoError(t, err)

	mr.Close() // L2 unreachable

	var calls atomic.Int64
	v, err := tc.GetOrLoad(ctx, "k2", time.Minute, strLoader("v2", &calls))
	require.NoError(t, err) // request must NOT fail
	assert.Equal(t, "v2", v)
	assert.Equal(t, int64(1), calls.Load())
	tc.Set(ctx, "k3", "v3", time.Minute) // must not panic or block
	tc.Invalidate(ctx, "k")              // must not panic or block
}
func TestTiered_BadJSONFallsThrough(t *testing.T) {
	mr := miniredis.RunT(t)
	tc := newTieredForTest[string](t, mr, "test")
	ctx := context.Background()
	require.NoError(t, mr.Set("test:k", "not-json"))

	var calls atomic.Int64
	v, err := tc.GetOrLoad(ctx, "k", time.Minute, strLoader("v1", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v1", v)
	assert.Equal(t, int64(1), calls.Load())
}

func TestTiered_Singleflight(t *testing.T) {
	mr := miniredis.RunT(t)
	tc := newTieredForTest[string](t, mr, "test")
	var calls atomic.Int64
	loader := func(context.Context) (string, error) {
		calls.Add(1)
		time.Sleep(50 * time.Millisecond)
		return "v1", nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := tc.GetOrLoad(context.Background(), "k", time.Minute, loader)
			assert.NoError(t, err)
			assert.Equal(t, "v1", v)
		}()
	}
	wg.Wait()
	assert.Equal(t, int64(1), calls.Load())
}

func TestTiered_L1OnlyMode(t *testing.T) {
	tc := NewTiered[string](TieredOptions{Name: "test", L1MaxSize: 10, L1TTL: time.Minute})
	var calls atomic.Int64
	v, err := tc.GetOrLoad(context.Background(), "k", time.Minute, strLoader("v1", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v1", v)
	v, err = tc.GetOrLoad(context.Background(), "k", time.Minute, strLoader("NO", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v1", v)
	assert.Equal(t, int64(1), calls.Load())
	tc.Set(context.Background(), "k", "v2", time.Minute)
	tc.Invalidate(context.Background(), "k")
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cache/ -run TestTiered -count=1`
Expected: FAIL（类型未定义）

- [ ] **Step 3: 实现 `redis_client.go`**

```go
// (license header)

package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

type universalClientAdapter struct{ client redis.UniversalClient }

// WrapRedisClient adapts a go-redis client to the RedisCacheClient interface.
// redis.Nil (key missing) is normalized to ("", nil) — a cache miss.
func WrapRedisClient(client redis.UniversalClient) RedisCacheClient {
	return &universalClientAdapter{client: client}
}

func (a *universalClientAdapter) Set(ctx context.Context, key string, value any, expiration time.Duration) error {
	return a.client.Set(ctx, key, value, expiration).Err()
}

func (a *universalClientAdapter) Get(ctx context.Context, key string) (string, error) {
	val, err := a.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return val, err
}

func (a *universalClientAdapter) Del(ctx context.Context, keys ...string) error {
	return a.client.Del(ctx, keys...).Err()
}

func (a *universalClientAdapter) Exists(ctx context.Context, keys ...string) (int64, error) {
	return a.client.Exists(ctx, keys...).Result()
}
```

- [ ] **Step 4: 实现 `tiered.go`**

```go
// (license header)

package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/e2bgateway/e2bgateway/internal/metrics"
)

// Hot-path TTLs from design doc §15.2.
const (
	SandboxMetadataTTL = 30 * time.Second
	TemplateListTTL    = 60 * time.Second
	BackendHealthTTL   = 10 * time.Second
)

// Broadcaster publishes cross-replica invalidations (implemented by *Invalidator).
type Broadcaster interface {
	Broadcast(ctx context.Context, cacheName, key string)
}

// TieredOptions configures a TieredCache. L2 nil => L1-only mode.
type TieredOptions struct {
	Name        string        // metrics label: sandbox_metadata / template_list
	L1MaxSize   int
	L1TTL       time.Duration
	L2          *RedisCache
	Timeout     time.Duration // per-L2-operation timeout, default 200ms
	Invalidator Broadcaster   // nil => no cross-replica broadcast
}

// TieredCache composes L1 (in-memory LRU) and L2 (shared Redis) with
// write-through writes and Pub/Sub cross-replica invalidation. Any L2
// failure degrades to L1 + backend load; it never fails a request.
type TieredCache[T any] struct {
	name    string
	l1      *Cache
	l2      *RedisCache
	timeout time.Duration
	inv     Broadcaster
	sf      singleflight.Group

	mu       sync.Mutex
	lastWarn time.Time
}

// NewTiered creates a tiered cache. T must be JSON-serializable for L2.
func NewTiered[T any](opts TieredOptions) *TieredCache[T] {
	if opts.Timeout <= 0 {
		opts.Timeout = 200 * time.Millisecond
	}
	return &TieredCache[T]{
		name:    opts.Name,
		l1:      New(opts.L1MaxSize, opts.L1TTL),
		l2:      opts.L2,
		timeout: opts.Timeout,
		inv:     opts.Invalidator,
	}
}

// GetOrLoad reads L1 → L2 (backfilling L1) → load (backfilling L2 then L1).
// Concurrent loads of the same key are merged via singleflight.
func (tc *TieredCache[T]) GetOrLoad(ctx context.Context, key string, ttl time.Duration, load func(context.Context) (T, error)) (T, error) {
	var zero T
	if v, ok := tc.l1.Get(key); ok {
		metrics.RecordCache(tc.name, "l1_hit")
		return v.(T), nil //nolint:errcheck // we control what is stored
	}
	if tc.l2 != nil {
		if raw, hit := tc.l2Get(ctx, key); hit {
			var v T
			if err := json.Unmarshal([]byte(raw), &v); err != nil {
				tc.warn("decode", fmt.Errorf("key %q: %w", key, err))
			} else {
				tc.l1.SetWithTTL(key, v, ttl)
				metrics.RecordCache(tc.name, "l2_hit")
				return v, nil
			}
		}
	}
	metrics.RecordCache(tc.name, "miss")
	v, err, _ := tc.sf.Do(key, func() (any, error) {
		val, loadErr := load(ctx)
		if loadErr != nil {
			return nil, loadErr
		}
		tc.fill(ctx, key, val, ttl)
		return val, nil
	})
	if err != nil {
		return zero, err
	}
	return v.(T), nil //nolint:errcheck // singleflight returns what load produced
}

// Set writes through: L2 first, then L1, then broadcasts an invalidation so
// other replicas drop their (now stale) L1 copy and re-read from L2.
func (tc *TieredCache[T]) Set(ctx context.Context, key string, val T, ttl time.Duration) {
	tc.fill(ctx, key, val, ttl)
	tc.broadcast(ctx, key)
}

// Invalidate removes the key from both tiers and broadcasts the removal.
func (tc *TieredCache[T]) Invalidate(ctx context.Context, key string) {
	if tc.l2 != nil {
		cctx, cancel := context.WithTimeout(ctx, tc.timeout)
		err := tc.l2.Delete(cctx, key)
		cancel()
		if err != nil {
			metrics.RecordCache(tc.name, "l2_error")
			tc.warn("delete", err)
		}
	}
	tc.l1.Delete(key)
	tc.broadcast(ctx, key)
}

// OnRemoteInvalidate applies an invalidation broadcast from another replica.
// Registered with Invalidator.Register under the cache's Name.
func (tc *TieredCache[T]) OnRemoteInvalidate(key string) {
	tc.l1.Delete(key)
	metrics.RecordCache(tc.name, "invalidation")
}

func (tc *TieredCache[T]) l2Get(ctx context.Context, key string) (string, bool) {
	cctx, cancel := context.WithTimeout(ctx, tc.timeout)
	defer cancel()
	raw, hit, err := tc.l2.Get(cctx, key)
	if err != nil {
		metrics.RecordCache(tc.name, "l2_error")
		tc.warn("get", err)
		return "", false
	}
	return raw, hit
}

// fill backfills L2 first, then L1 (spec §3.2). L2 failures are logged and
// metered but never propagated.
func (tc *TieredCache[T]) fill(ctx context.Context, key string, val T, ttl time.Duration) {
	if tc.l2 != nil {
		raw, err := json.Marshal(val)
		if err != nil {
			metrics.RecordCache(tc.name, "l2_error")
			tc.warn("encode", err)
		} else {
			cctx, cancel := context.WithTimeout(ctx, tc.timeout)
			err = tc.l2.SetWithTTL(cctx, key, string(raw), ttl)
			cancel()
			if err != nil {
				metrics.RecordCache(tc.name, "l2_error")
				tc.warn("set", err)
			}
		}
	}
	tc.l1.SetWithTTL(key, val, ttl)
}

func (tc *TieredCache[T]) broadcast(ctx context.Context, key string) {
	if tc.inv == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, tc.timeout)
	defer cancel()
	tc.inv.Broadcast(cctx, tc.name, key)
}

// warn logs L2 degradation at most once per 10s per cache instance.
func (tc *TieredCache[T]) warn(op string, err error) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if time.Since(tc.lastWarn) < 10*time.Second {
		return
	}
	tc.lastWarn = time.Now()
	slog.Warn("tiered cache L2 degraded", "cache", tc.name, "op", op, "error", err)
}
```

- [ ] **Step 5: `go mod tidy` + 跑测试**

Run: `go mod tidy && go test -race ./internal/cache/ -count=1`
Expected: PASS（`golang.org/x/sync` 变为直接依赖；既有 cache/redis 测试不受影响）

- [ ] **Step 6: Commit**

```bash
git add internal/cache/tiered.go internal/cache/redis_client.go internal/cache/tiered_test.go go.mod go.sum
git commit -s -m "feat(cache): generic TieredCache composing L1 LRU and L2 Redis (#113)"
```

---

### Task 4: `Invalidator` — Pub/Sub 跨副本失效

**Files:**
- Create: `internal/cache/invalidator.go`
- Test: `internal/cache/invalidator_test.go`（新建）

**Interfaces:**
- Consumes: `TieredCache.OnRemoteInvalidate`（Task 3）、`metrics.RecordCache`
- Produces: `func NewInvalidator(client redis.UniversalClient, channel string) *Invalidator`；`func (inv *Invalidator) Register(name string, fn func(string))`；`func (inv *Invalidator) Broadcast(ctx context.Context, cacheName, key string)`（实现 `Broadcaster`）；`func (inv *Invalidator) Run(ctx context.Context)`（阻塞，ctx 取消退出）；`func (inv *Invalidator) Ready() <-chan struct{}`（首次订阅建立后 close，测试/启动时序等待用）。

- [ ] **Step 1: 写失败测试**

`internal/cache/invalidator_test.go`（package cache，license header）：

```go
package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// replica bundles one cache + invalidator, as a gateway pod would have.
type replica struct {
	tc  *TieredCache[string]
	inv *Invalidator
}

func newReplica(t *testing.T, mr *miniredis.Miniredis) replica {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	inv := NewInvalidator(client, "test:invalidate")
	tc := NewTiered[string](TieredOptions{
		Name: "template_list", L1MaxSize: 100, L1TTL: time.Minute,
		L2:          NewRedisCache(WrapRedisClient(client), "test:", time.Minute),
		Invalidator: inv,
	})
	inv.Register("template_list", tc.OnRemoteInvalidate)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go inv.Run(ctx)
	select {
	case <-inv.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("invalidator subscriber not ready")
	}
	return replica{tc: tc, inv: inv}
}

func TestInvalidator_SetBroadcastClearsRemoteL1(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newReplica(t, mr)
	b := newReplica(t, mr)
	ctx := context.Background()

	// Warm both L1s with v1.
	for _, r := range []replica{a, b} {
		_, err := r.tc.GetOrLoad(ctx, "tmpl:mock", time.Minute, strLoader("v1", nil))
		require.NoError(t, err)
	}

	// A writes v2 (write-through + broadcast).
	a.tc.Set(ctx, "tmpl:mock", "v2", time.Minute)

	// B sees v2 within 1s (issue #113 acceptance).
	require.Eventually(t, func() bool {
		v, err := b.tc.GetOrLoad(ctx, "tmpl:mock", time.Minute, strLoader("SHOULD-NOT-LOAD", nil))
		return err == nil && v == "v2"
	}, time.Second, 10*time.Millisecond)

	// A's own L1 must NOT be cleared by its own broadcast.
	v, err := a.tc.GetOrLoad(ctx, "tmpl:mock", time.Minute, strLoader("NO", nil))
	require.NoError(t, err)
	assert.Equal(t, "v2", v)
}

func TestInvalidator_InvalidateBroadcast(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newReplica(t, mr)
	b := newReplica(t, mr)
	ctx := context.Background()

	for _, r := range []replica{a, b} {
		_, err := r.tc.GetOrLoad(ctx, "tmpl:mock", time.Minute, strLoader("v1", nil))
		require.NoError(t, err)
	}

	a.tc.Invalidate(ctx, "tmpl:mock")

	// B's next read must go to the loader (L1 cleared, L2 deleted).
	require.Eventually(t, func() bool {
		v, err := b.tc.GetOrLoad(ctx, "tmpl:mock", time.Minute, strLoader("v3", nil))
		return err == nil && v == "v3"
	}, time.Second, 10*time.Millisecond)
}

func TestInvalidator_UnknownCacheIgnored(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newReplica(t, mr)
	// Broadcast for an unregistered cache name must not panic.
	a.inv.Broadcast(context.Background(), "no_such_cache", "k")
	time.Sleep(50 * time.Millisecond)
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cache/ -run TestInvalidator -count=1`
Expected: FAIL（`Invalidator` 未定义）

- [ ] **Step 3: 实现 `invalidator.go`**

```go
// (license header)

package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/e2bgateway/e2bgateway/internal/metrics"
)

// invalidateMsg is the Pub/Sub payload. It carries only names/keys, never
// cached values. Sender lets each replica skip its own broadcasts.
type invalidateMsg struct {
	Cache  string `json:"cache"`
	Key    string `json:"key"`
	Sender string `json:"sender"`
}

// Invalidator broadcasts cache invalidations over Redis Pub/Sub and applies
// remote invalidations to registered caches. One per process; Run must be
// started in a goroutine.
type Invalidator struct {
	client     redis.UniversalClient
	channel    string
	instanceID string

	mu       sync.RWMutex
	handlers map[string]func(string)

	ready     chan struct{}
	readyOnce sync.Once
}

// NewInvalidator creates an invalidator. instanceID = hostname + random
// suffix so replicas (and tests in one process) never collide.
func NewInvalidator(client redis.UniversalClient, channel string) *Invalidator {
	host, _ := os.Hostname()
	var b [8]byte
	_, _ = rand.Read(b[:])
	return &Invalidator{
		client:     client,
		channel:    channel,
		instanceID: host + "-" + hex.EncodeToString(b[:]),
		handlers:   make(map[string]func(string)),
		ready:      make(chan struct{}),
	}
}

// Register attaches a cache's OnRemoteInvalidate under its metrics name.
func (inv *Invalidator) Register(name string, fn func(string)) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	inv.handlers[name] = fn
}

// Ready is closed once the subscription is first established. Callers that
// need broadcast delivery guarantees (tests, startup sequencing) wait on it.
func (inv *Invalidator) Ready() <-chan struct{} { return inv.ready }

// Broadcast publishes an invalidation. Failures are logged + metered, never
// propagated (implements Broadcaster).
func (inv *Invalidator) Broadcast(ctx context.Context, cacheName, key string) {
	raw, err := json.Marshal(invalidateMsg{Cache: cacheName, Key: key, Sender: inv.instanceID})
	if err != nil {
		return // static struct; cannot fail in practice
	}
	if err := inv.client.Publish(ctx, inv.channel, raw).Err(); err != nil {
		metrics.RecordCache(cacheName, "l2_error")
		slog.Warn("cache invalidation broadcast failed", "cache", cacheName, "key", key, "error", err)
	}
}

// Run consumes invalidation broadcasts until ctx is cancelled. go-redis
// reconnects the subscription internally; if the channel still closes, an
// outer loop re-subscribes with exponential backoff (1s → 30s). While
// disconnected, coherence degrades to L1 TTL expiry (bounded staleness).
func (inv *Invalidator) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		sub := inv.client.Subscribe(ctx, inv.channel)
		if err := sub.Ping(ctx); err == nil {
			inv.readyOnce.Do(func() { close(inv.ready) })
		}
		ch := sub.Channel()
		dropped := false
		for !dropped && ctx.Err() == nil {
			select {
			case <-ctx.Done():
			case msg, ok := <-ch:
				if !ok {
					dropped = true
					break
				}
				backoff = time.Second
				inv.handle(msg.Payload)
			}
		}
		_ = sub.Close()
		if ctx.Err() != nil {
			return
		}
		slog.Warn("cache invalidation subscription dropped; reconnecting",
			"channel", inv.channel, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (inv *Invalidator) handle(payload string) {
	var msg invalidateMsg
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		return
	}
	if msg.Sender == inv.instanceID {
		return
	}
	inv.mu.RLock()
	fn := inv.handlers[msg.Cache]
	inv.mu.RUnlock()
	if fn != nil {
		fn(msg.Key)
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test -race ./internal/cache/ -count=1`
Expected: PASS（全部 cache 测试，含 Task 3）

- [ ] **Step 5: Commit**

```bash
git add internal/cache/invalidator.go internal/cache/invalidator_test.go
git commit -s -m "feat(cache): Redis Pub/Sub cross-replica invalidation (#113)"
```

---

### Task 5: Sandbox 热点路径接入（handler 变体）

**Files:**
- Create: `internal/api/v1/cache_keys.go`
- Modify: `internal/api/v1/handlers.go`（`CreateSandboxHandler` :42、`GetSandboxHandler` :169、`KillSandboxHandler` :190、`PauseSandboxHandler` :213、`ResumeSandboxHandler` :233、`SetTimeoutHandler` :253）
- Test: `internal/api/v1/handlers_cache_test.go`（新建，package v1_test，复用 `setupTestRouter`）

**Interfaces:**
- Consumes: `cache.NewTiered[dto.SandboxInfo]`、`GetOrLoad/Set/Invalidate`、`cache.SandboxMetadataTTL`（Task 3）；现有 `resolveAdapterForSandbox`、`sandboxToDTO`
- Produces: `v1.GetSandboxHandlerWithCache(registry *adapter.Registry, router *routing.Router, sbxCache *cache.TieredCache[dto.SandboxInfo]) http.HandlerFunc`；同参数模式的 `CreateSandboxHandlerWithCache(registry, router, envdDomain string, sbxCache ...)`、`KillSandboxHandlerWithCache`、`PauseSandboxHandlerWithCache`、`ResumeSandboxHandlerWithCache`、`SetTimeoutHandlerWithCache`。原构造函数保留签名，改为一行委托（nil cache）。key 函数 `sandboxCacheKey(backend, sandboxID) = "sbx:"+backend+":"+sandboxID`。Task 8 在路由中调用这些变体。

- [ ] **Step 1: 写失败测试**

`internal/api/v1/handlers_cache_test.go`（license header；imports 参照 `handlers_test.go`，另加 `internal/cache`、`chi`、`context`）：

```go
package v1_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/e2bgateway/e2bgateway/internal/api/dto"
	v1 "github.com/e2bgateway/e2bgateway/internal/api/v1"
	"github.com/e2bgateway/e2bgateway/internal/cache"
)

func newSandboxCacheForTest() *cache.TieredCache[dto.SandboxInfo] {
	return cache.NewTiered[dto.SandboxInfo](cache.TieredOptions{
		Name: "sandbox_metadata", L1MaxSize: 100, L1TTL: time.Minute,
	})
}

func sandboxReq(method, target, id, body string) *http.Request {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	if id != "" {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("sandboxID", id)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	}
	return req
}

func TestGetSandboxHandler_CacheHitAndInvalidation(t *testing.T) {
	reg, r := setupTestRouter()
	sbxCache := newSandboxCacheForTest()

	// Create a sandbox.
	w := httptest.NewRecorder()
	v1.CreateSandboxHandler(reg, r, "")(w, sandboxReq(http.MethodPost, "/sandboxes", "", `{"templateID":"base","timeout":300}`))
	require.Equal(t, http.StatusCreated, w.Code)
	var created dto.SandboxCreateResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	id := created.SandboxID

	// First GET populates the cache.
	w = httptest.NewRecorder()
	v1.GetSandboxHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodGet, "/sandboxes/"+id, id, ""))
	require.Equal(t, http.StatusOK, w.Code)

	// Kill DIRECTLY through the adapter (bypassing handler invalidation):
	// the cached GET must still succeed — proves the read was served from cache.
	a := reg.List()[0]
	require.NoError(t, a.KillSandbox(context.Background(), id))
	w = httptest.NewRecorder()
	v1.GetSandboxHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodGet, "/sandboxes/"+id, id, ""))
	assert.Equal(t, http.StatusOK, w.Code)

	// Uncached handler sees the truth: 404.
	w = httptest.NewRecorder()
	v1.GetSandboxHandler(reg, r)(w, sandboxReq(http.MethodGet, "/sandboxes/"+id, id, ""))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestKillSandboxHandler_InvalidatesCache(t *testing.T) {
	reg, r := setupTestRouter()
	sbxCache := newSandboxCacheForTest()

	w := httptest.NewRecorder()
	v1.CreateSandboxHandlerWithCache(reg, r, "", sbxCache)(w, sandboxReq(http.MethodPost, "/sandboxes", "", `{"templateID":"base","timeout":300}`))
	require.Equal(t, http.StatusCreated, w.Code)
	var created dto.SandboxCreateResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	id := created.SandboxID

	// Warm the cache.
	w = httptest.NewRecorder()
	v1.GetSandboxHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodGet, "/sandboxes/"+id, id, ""))
	require.Equal(t, http.StatusOK, w.Code)

	// Kill through the cache-aware handler → invalidation.
	w = httptest.NewRecorder()
	v1.KillSandboxHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodDelete, "/sandboxes/"+id, id, ""))
	require.Equal(t, http.StatusNoContent, w.Code)

	// Next cached GET must miss and 404.
	w = httptest.NewRecorder()
	v1.GetSandboxHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodGet, "/sandboxes/"+id, id, ""))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestPauseResumeSetTimeout_InvalidateCache(t *testing.T) {
	reg, r := setupTestRouter()
	sbxCache := newSandboxCacheForTest()

	w := httptest.NewRecorder()
	v1.CreateSandboxHandler(reg, r, "")(w, sandboxReq(http.MethodPost, "/sandboxes", "", `{"templateID":"base","timeout":300}`))
	require.Equal(t, http.StatusCreated, w.Code)
	var created dto.SandboxCreateResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	id := created.SandboxID

	warm := func() {
		w := httptest.NewRecorder()
		v1.GetSandboxHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodGet, "/sandboxes/"+id, id, ""))
		require.Equal(t, http.StatusOK, w.Code)
	}

	// Pause invalidates → state reflects "paused" on next read.
	warm()
	w = httptest.NewRecorder()
	v1.PauseSandboxHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodPost, "/sandboxes/"+id+"/pause", id, ""))
	require.Equal(t, http.StatusNoContent, w.Code)
	w = httptest.NewRecorder()
	v1.GetSandboxHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodGet, "/sandboxes/"+id, id, ""))
	require.Equal(t, http.StatusOK, w.Code)
	var info dto.SandboxInfo
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &info))
	assert.Equal(t, "paused", info.State)

	// Resume write-through → state "running" visible immediately.
	w = httptest.NewRecorder()
	v1.ResumeSandboxHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodPost, "/sandboxes/"+id+"/resume", id, ""))
	require.Equal(t, http.StatusOK, w.Code)
	w = httptest.NewRecorder()
	v1.GetSandboxHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodGet, "/sandboxes/"+id, id, ""))
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &info))
	assert.Equal(t, "running", info.State)

	// SetTimeout invalidates (EndAt is part of cached metadata).
	w = httptest.NewRecorder()
	v1.SetTimeoutHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodPatch, "/sandboxes/"+id+"/timeout", id, `{"timeout":600}`))
	require.Equal(t, http.StatusNoContent, w.Code)
}
```

注意：mock adapter 的 pause/resume 行为以既有 `TestPauseSandboxHandler`（handlers_test.go:654 附近）为准；若 mock 的 resume 返回状态不是 "running"，按 mock 实际行为调整断言（先读 `internal/adapter/mock/adapter.go` 的 Pause/Resume 实现再写断言）。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api/v1/ -run 'Cache|Invalidat' -count=1`
Expected: FAIL（WithCache 变体未定义）

- [ ] **Step 3: 实现 `cache_keys.go`**

```go
// (license header)

package v1

// sandboxCacheKey namespaces cached sandbox metadata by backend so the same
// sandbox ID on two backends never collides.
func sandboxCacheKey(backend, sandboxID string) string {
	return "sbx:" + backend + ":" + sandboxID
}

// templateCacheKey is the per-backend template list cache key.
func templateCacheKey(backend string) string {
	return "tmpl:" + backend
}
```

- [ ] **Step 4: 改造 handlers.go**

模式（每个 handler 相同）：原构造函数改名加 `WithCache` 并追加 cache 参数，原签名保留为一行委托。以 Get 为例（完整替换 `GetSandboxHandler`）：

```go
// GetSandboxHandler handles GET /sandboxes/{sandboxID} (uncached).
func GetSandboxHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return GetSandboxHandlerWithCache(registry, router, nil)
}

// GetSandboxHandlerWithCache is GetSandboxHandler with an optional tiered
// cache (nil = pass-through). Cached values carry no access tokens.
func GetSandboxHandlerWithCache(registry *adapter.Registry, router *routing.Router, sbxCache *cache.TieredCache[dto.SandboxInfo]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		a, err := resolveAdapterForSandbox(r.Context(), registry, sandboxID)
		if err != nil {
			writeAdapterError(w, err, http.StatusInternalServerError)
			return
		}

		load := func(ctx context.Context) (dto.SandboxInfo, error) {
			sb, err := a.GetSandbox(ctx, sandboxID)
			if err != nil {
				return dto.SandboxInfo{}, err
			}
			return *sandboxToDTO(sb), nil
		}
		var info dto.SandboxInfo
		if sbxCache != nil {
			info, err = sbxCache.GetOrLoad(r.Context(), sandboxCacheKey(a.Name(), sandboxID), cache.SandboxMetadataTTL, load)
		} else {
			info, err = load(r.Context())
		}
		if err != nil {
			writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
			return
		}
		writeJSON(w, http.StatusOK, &info)
	}
}
```

其余五个 handler 的改动点（保持原逻辑逐行不变，只加 cache 分支）：

- `CreateSandboxHandlerWithCache(registry, router, envdDomain string, sbxCache *cache.TieredCache[dto.SandboxInfo])`：在 `metrics.RecordLifecycle(backendName, "create", "success")` 之后加：
  ```go
  if sbxCache != nil {
      sbxCache.Set(r.Context(), sandboxCacheKey(backendName, sandbox.SandboxID), *sandboxToDTO(sandbox), cache.SandboxMetadataTTL)
  }
  ```
- `KillSandboxHandlerWithCache`：成功分支 `metrics.RecordLifecycle(a.Name(), "delete", "success")` 之后加 `if sbxCache != nil { sbxCache.Invalidate(r.Context(), sandboxCacheKey(a.Name(), sandboxID)) }`
- `PauseSandboxHandlerWithCache`：`if err := a.PauseSandbox(...); err == nil {` 分支内、`RecordLifecycle` 之后加同样的 `Invalidate`。
- `ResumeSandboxHandlerWithCache`：成功分支内加 `if sbxCache != nil { sbxCache.Set(r.Context(), sandboxCacheKey(a.Name(), sandboxID), *sandboxToDTO(sandbox), cache.SandboxMetadataTTL) }`
- `SetTimeoutHandlerWithCache`：`if err := a.SetTimeout(...); err == nil {` 分支内加 `Invalidate`（EndAt 属于缓存元数据）。

import 块加 `"github.com/e2bgateway/e2bgateway/internal/cache"`。

- [ ] **Step 5: 跑测试确认通过（含全部既有 handler 测试）**

Run: `go test -race ./internal/api/v1/ -count=1`
Expected: PASS（既有 ~60 处旧签名调用点零改动）

- [ ] **Step 6: Commit**

```bash
git add internal/api/v1/cache_keys.go internal/api/v1/handlers.go internal/api/v1/handlers_cache_test.go
git commit -s -m "feat(api): tiered cache on sandbox metadata hot path (#113)"
```

---

### Task 6: Template list 热点路径接入

**Files:**
- Modify: `internal/api/v1/handlers.go`（`ListTemplatesHandler` :633、`ListTemplatesHandlerV2` :653、`CreateTemplateHandler` :714、`CreateTemplateHandlerV2` :747、`DeleteTemplateHandler` :782、`UpdateTemplateHandler` :1346）
- Test: `internal/api/v1/handlers_cache_test.go`（追加）

**Interfaces:**
- Consumes: `cache.TieredCache[[]*adapter.Template]`、`cache.TemplateListTTL`、`templateCacheKey`（Task 5）
- Produces: `v1.ListTemplatesHandlerWithCache(registry, router, tmplCache *cache.TieredCache[[]*adapter.Template])`、`ListTemplatesHandlerV2WithCache`（同参）、`CreateTemplateHandlerWithCache` / `CreateTemplateHandlerV2WithCache` / `DeleteTemplateHandlerWithCache` / `UpdateTemplateHandlerWithCache`（同参）。Task 8 路由调用。

- [ ] **Step 1: 写失败测试（追加到 handlers_cache_test.go）**

```go
func newTemplateCacheForTest() *cache.TieredCache[[]*adapter.Template] {
	return cache.NewTiered[[]*adapter.Template](cache.TieredOptions{
		Name: "template_list", L1MaxSize: 100, L1TTL: time.Minute,
	})
}

func TestListTemplatesHandler_CacheHit(t *testing.T) {
	reg, r := setupTestRouter()
	tmplCache := newTemplateCacheForTest()

	list := func() []dto.TemplateInfo {
		w := httptest.NewRecorder()
		v1.ListTemplatesHandlerWithCache(reg, r, tmplCache)(w, httptest.NewRequest(http.MethodGet, "/templates", nil))
		require.Equal(t, http.StatusOK, w.Code)
		var out []dto.TemplateInfo
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		return out
	}
	before := list()

	// Create a template DIRECTLY through the adapter (bypass invalidation).
	a := reg.List()[0]
	_, err := a.CreateTemplate(context.Background(), &adapter.CreateTemplateRequest{Name: "cache-test-tmpl"})
	require.NoError(t, err)

	// Cached list must be unchanged (proves cache hit).
	assert.Len(t, list(), len(before))

	// Uncached list sees the new template.
	w := httptest.NewRecorder()
	v1.ListTemplatesHandler(reg, r)(w, httptest.NewRequest(http.MethodGet, "/templates", nil))
	var fresh []dto.TemplateInfo
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &fresh))
	assert.Len(t, fresh, len(before)+1)
}

func TestCreateTemplateHandler_InvalidatesCache(t *testing.T) {
	reg, r := setupTestRouter()
	tmplCache := newTemplateCacheForTest()

	list := func() int {
		w := httptest.NewRecorder()
		v1.ListTemplatesHandlerWithCache(reg, r, tmplCache)(w, httptest.NewRequest(http.MethodGet, "/templates", nil))
		require.Equal(t, http.StatusOK, w.Code)
		var out []dto.TemplateInfo
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		return len(out)
	}
	before := list()

	// Create through the cache-aware handler → invalidation broadcast path.
	w := httptest.NewRecorder()
	v1.CreateTemplateHandlerWithCache(reg, r, tmplCache)(w, sandboxReq(http.MethodPost, "/templates", "", `{"name":"cache-test-tmpl2"}`))
	require.Equal(t, http.StatusAccepted, w.Code)

	assert.Equal(t, before+1, list())
}

func TestDeleteTemplateHandler_InvalidatesCache(t *testing.T) {
	reg, r := setupTestRouter()
	tmplCache := newTemplateCacheForTest()
	a := reg.List()[0]
	build, err := a.CreateTemplate(context.Background(), &adapter.CreateTemplateRequest{Name: "cache-test-del"})
	require.NoError(t, err)

	// Warm cache.
	w := httptest.NewRecorder()
	v1.ListTemplatesHandlerWithCache(reg, r, tmplCache)(w, httptest.NewRequest(http.MethodGet, "/templates", nil))
	require.Equal(t, http.StatusOK, w.Code)

	// Delete through cache-aware handler.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("templateID", build.TemplateID)
	req := httptest.NewRequest(http.MethodDelete, "/templates/"+build.TemplateID, nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w = httptest.NewRecorder()
	v1.DeleteTemplateHandlerWithCache(reg, r, tmplCache)(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)

	// Next cached list must not contain the deleted template.
	w = httptest.NewRecorder()
	v1.ListTemplatesHandlerWithCache(reg, r, tmplCache)(w, httptest.NewRequest(http.MethodGet, "/templates", nil))
	var out []dto.TemplateInfo
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	for _, ti := range out {
		assert.NotEqual(t, build.TemplateID, ti.TemplateID)
	}
}
```

imports 追加 `"github.com/e2bgateway/e2bgateway/internal/adapter"`。mock adapter 的 `CreateTemplate` 返回 `*adapter.TemplateBuild`（以 handlers.go:731 用法为准：`build.TemplateID`/`build.BuildID`/`build.Status`）；若字段名不符，先读 mock 实现再调整。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api/v1/ -run 'Templates.*Cache|Template.*Invalidat' -count=1`
Expected: FAIL

- [ ] **Step 3: 实现**

List（v1/v2 同模式，保留各自 DTO 映射）：

```go
// ListTemplatesHandler handles GET /templates (uncached).
func ListTemplatesHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return ListTemplatesHandlerWithCache(registry, router, nil)
}

// ListTemplatesHandlerWithCache caches each backend's raw template list
// (60s TTL); merging and DTO conversion stay per-request (cheap).
func ListTemplatesHandlerWithCache(registry *adapter.Registry, router *routing.Router, tmplCache *cache.TieredCache[[]*adapter.Template]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var allTemplates []*dto.TemplateInfo
		for _, a := range registry.List() {
			load := func(ctx context.Context) ([]*adapter.Template, error) {
				return a.ListTemplates(ctx, adapter.ListOptions{})
			}
			var templates []*adapter.Template
			var err error
			if tmplCache != nil {
				templates, err = tmplCache.GetOrLoad(r.Context(), templateCacheKey(a.Name()), cache.TemplateListTTL, load)
			} else {
				templates, err = load(r.Context())
			}
			if err != nil {
				continue
			}
			for _, t := range templates {
				allTemplates = append(allTemplates, templateToDTO(t))
			}
		}
		if allTemplates == nil {
			allTemplates = []*dto.TemplateInfo{}
		}
		writeJSON(w, http.StatusOK, allTemplates)
	}
}
```

V2 相同骨架，循环体用既有 `dto.V2TemplateInfo{...}` 映射。

 mutation handlers：原签名委托 + 成功分支加失效。`CreateTemplateHandlerWithCache` / `CreateTemplateHandlerV2WithCache` 在 `if err == nil {` 成功分支 writeJSON 之前加：

```go
			if tmplCache != nil {
				tmplCache.Invalidate(r.Context(), templateCacheKey(a.Name()))
			}
```

`DeleteTemplateHandlerWithCache` 同样在成功分支加。`UpdateTemplateHandlerWithCache`：把现有 `UpdateTemplateHandler` 整体改名为 WithCache 版本（保留全部错误优先级逻辑不动），在 `updated, err := a.UpdateTemplate(...)` 的 `err == nil` 分支 writeJSON 之前加同样的 Invalidate；原签名一行委托。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test -race ./internal/api/v1/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api/v1/handlers.go internal/api/v1/handlers_cache_test.go
git commit -s -m "feat(api): tiered cache on template list hot path (#113)"
```

---

### Task 7: /readyz 健康聚合仅-L1 缓存

**Files:**
- Modify: `internal/api/v1/handlers.go`（`ReadyHandler` :1210）
- Test: `internal/api/v1/handlers_cache_test.go`（追加）

**Interfaces:**
- Consumes: 现有 `*cache.Cache`、`cache.BackendHealthTTL`
- Produces: `v1.ReadyHandlerWithCache(registry *adapter.Registry, healthCache *cache.Cache) http.HandlerFunc`；`ReadyHandler` 保留签名委托 nil。Task 8 路由调用。

- [ ] **Step 1: 写失败测试**

```go
func TestReadyHandlerWithCache(t *testing.T) {
	reg, _ := setupTestRouter()
	hc := cache.New(10, cache.BackendHealthTTL)

	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		v1.ReadyHandlerWithCache(reg, hc)(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		require.Equal(t, http.StatusOK, w.Code)
	}
	// Aggregated result is cached under "readyz".
	v, ok := hc.Get("readyz")
	require.True(t, ok)
	results, ok := v.(map[string]error)
	require.True(t, ok)
	assert.Contains(t, results, "mock")
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api/v1/ -run TestReadyHandlerWithCache -count=1`
Expected: FAIL

- [ ] **Step 3: 实现**

```go
// ReadyHandler handles GET /readyz (uncached).
func ReadyHandler(registry *adapter.Registry) http.HandlerFunc {
	return ReadyHandlerWithCache(registry, nil)
}

// ReadyHandlerWithCache caches the aggregated backend health fan-out for
// BackendHealthTTL (10s, L1 only — health is per-pod observed state).
func ReadyHandlerWithCache(registry *adapter.Registry, healthCache *cache.Cache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var results map[string]error
		if healthCache != nil {
			if v, ok := healthCache.Get("readyz"); ok {
				results = v.(map[string]error) //nolint:errcheck // we control what is stored
			}
		}
		if results == nil {
			results = registry.HealthCheckAll(r.Context())
			if healthCache != nil {
				healthCache.SetWithTTL("readyz", results, cache.BackendHealthTTL)
			}
		}

		// —— 以下与原 ReadyHandler 完全一致 ——
		allHealthy := true
		backendStatus := make(map[string]string)
		for name, err := range results {
			if err != nil {
				allHealthy = false
				backendStatus[name] = "unhealthy: " + err.Error()
			} else {
				backendStatus[name] = "healthy"
			}
		}
		resp := map[string]any{"status": "ok", "backends": backendStatus}
		status := http.StatusOK
		if !allHealthy {
			resp["status"] = "degraded"
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, resp)
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test -race ./internal/api/v1/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api/v1/handlers.go internal/api/v1/handlers_cache_test.go
git commit -s -m "feat(api): 10s L1 cache for readyz health fan-out (#113)"
```

---

### Task 8: Server 接线（构造、注入、生命周期）

**Files:**
- Create: `internal/server/cache.go`
- Modify: `internal/server/http.go`（`Server` struct :54、`New` :83 内 metrics.Init 之后、`Stop` :274、`buildRouter` 路由表 :366-542）
- Test: `internal/server/cache_test.go`（新建）

**Interfaces:**
- Consumes: Task 1 配置类型、Task 3 `NewTiered`/`WrapRedisClient`/`NewRedisCache`、Task 4 `NewInvalidator`/`Register`/`Run`、Task 5/6/7 WithCache 变体、`redisx.NormalizeRedisAddr`/`TLSConfig`/`AddrRequiresTLS`
- Produces: `Server` 新字段 `sandboxCache *cache.TieredCache[dto.SandboxInfo]`、`templateCache *cache.TieredCache[[]*adapter.Template]`、`healthCache *cache.Cache`、`invalidator *cache.Invalidator`、`cacheCancel context.CancelFunc`、`cacheDone chan struct{}`；`func (s *Server) initCaches() error`。

- [ ] **Step 1: 写失败测试**

`internal/server/cache_test.go`（package server，license header）：

```go
package server

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/e2bgateway/e2bgateway/internal/config"
)

func tieredTestConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Cache.Tiered.Enabled = true
	cfg.Cache.Tiered.L1 = config.TieredL1Config{MaxSize: 100, DefaultTTL: 30 * time.Second}
	cfg.Cache.Tiered.L2 = config.TieredL2Config{Timeout: 100 * time.Millisecond, KeyPrefix: "e2bgw:cache:", PubsubChannel: "e2bgw:cache:invalidate"}
	return cfg
}

func TestInitCaches_Disabled(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	require.NoError(t, s.initCaches())
	assert.Nil(t, s.sandboxCache)
	assert.Nil(t, s.templateCache)
	assert.Nil(t, s.healthCache)
	assert.Nil(t, s.invalidator)
}

func TestInitCaches_L1Only(t *testing.T) {
	s := &Server{cfg: tieredTestConfig()}
	require.NoError(t, s.initCaches())
	assert.NotNil(t, s.sandboxCache)
	assert.NotNil(t, s.templateCache)
	assert.NotNil(t, s.healthCache)
	assert.Nil(t, s.invalidator) // no l2.address → no Pub/Sub
}

func TestInitCaches_WithRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	cfg := tieredTestConfig()
	cfg.Cache.Tiered.L2.Address = mr.Addr() // loopback → plaintext OK
	s := &Server{cfg: cfg}
	require.NoError(t, s.initCaches())
	require.NotNil(t, s.invalidator)
	require.NotNil(t, s.cacheCancel)
	s.cacheCancel()
	<-s.cacheDone // Run must exit promptly on cancel
}

func TestInitCaches_UnreachableRedisFailsStartup(t *testing.T) {
	mr := miniredis.RunT(t)
	addr := mr.Addr()
	mr.Close()
	cfg := tieredTestConfig()
	cfg.Cache.Tiered.L2.Address = addr
	s := &Server{cfg: cfg}
	err := s.initCaches()
	assert.ErrorContains(t, err, "unreachable")
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/server/ -run TestInitCaches -count=1`
Expected: FAIL

- [ ] **Step 3: 实现 `internal/server/cache.go`**

```go
// (license header)

package server

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/api/dto"
	"github.com/e2bgateway/e2bgateway/internal/cache"
	"github.com/e2bgateway/e2bgateway/internal/redisx"
)

// initCaches builds the tiered caches when cache.tiered.enabled. Disabled
// (default) leaves all cache fields nil → handlers pass through unchanged.
// Enabled without l2.address runs L1-only. An unreachable configured Redis
// fails startup (misconfiguration should surface immediately, matching the
// sessionstore precedent); runtime failures degrade to L1 per spec §3.3.
func (s *Server) initCaches() error {
	tc := s.cfg.Cache.Tiered
	if !tc.Enabled {
		return nil
	}

	s.healthCache = cache.New(100, cache.BackendHealthTTL)

	opts := cache.TieredOptions{
		L1MaxSize: tc.L1.MaxSize,
		L1TTL:     tc.L1.DefaultTTL,
		Timeout:   tc.L2.Timeout,
	}

	if tc.L2.Address != "" {
		addr, schemeTLS := redisx.NormalizeRedisAddr(tc.L2.Address)
		tlsCfg, err := redisx.TLSConfig(addr, tc.L2.TLS || schemeTLS, tc.L2.Password != "")
		if err != nil {
			return fmt.Errorf("tiered cache redis TLS config: %w", err)
		}
		// Cached payloads carry no credentials (verified: SandboxInfo and
		// Template have no token fields), so non-loopback plaintext is a
		// loud WARN rather than sessionstore's fail-closed. Password over
		// plaintext is already rejected by config validation.
		if tlsCfg == nil && redisx.AddrRequiresTLS(addr) {
			slog.Warn("tiered cache: non-loopback redis WITHOUT TLS; cached payloads carry no credentials but TLS is recommended",
				"op", "tiered_cache_init", "addr", tc.L2.Address)
		}
		client := redis.NewClient(&redis.Options{
			Addr:      addr,
			Password:  tc.L2.Password,
			DB:        tc.L2.DB,
			TLSConfig: tlsCfg,
		})
		pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Ping(pingCtx).Err(); err != nil {
			_ = client.Close()
			return fmt.Errorf("tiered cache redis unreachable at %s: %w", tc.L2.Address, err)
		}
		opts.L2 = cache.NewRedisCache(cache.WrapRedisClient(client), tc.L2.KeyPrefix, tc.L1.DefaultTTL)
		inv := cache.NewInvalidator(client, tc.L2.PubsubChannel)
		opts.Invalidator = inv
		s.invalidator = inv
	}

	sbOpts := opts
	sbOpts.Name = "sandbox_metadata"
	s.sandboxCache = cache.NewTiered[dto.SandboxInfo](sbOpts)

	tmplOpts := opts
	tmplOpts.Name = "template_list"
	s.templateCache = cache.NewTiered[[]*adapter.Template](tmplOpts)

	if s.invalidator != nil {
		s.invalidator.Register("sandbox_metadata", s.sandboxCache.OnRemoteInvalidate)
		s.invalidator.Register("template_list", s.templateCache.OnRemoteInvalidate)
		ctx, cancel := context.WithCancel(context.Background())
		s.cacheCancel = cancel
		s.cacheDone = make(chan struct{})
		go func() {
			defer close(s.cacheDone)
			s.invalidator.Run(ctx)
		}()
	}

	slog.Info("tiered cache enabled", "l2", tc.L2.Address != "", "l1MaxSize", tc.L1.MaxSize)
	return nil
}
```

- [ ] **Step 4: 接线 `http.go`**

(a) `Server` struct 追加字段（`buildInfo` 之后）：

```go
	sandboxCache  *cache.TieredCache[dto.SandboxInfo]
	templateCache *cache.TieredCache[[]*adapter.Template]
	healthCache   *cache.Cache
	invalidator   *cache.Invalidator
	cacheCancel   context.CancelFunc
	cacheDone     chan struct{}
```

import 加 `"github.com/e2bgateway/e2bgateway/internal/cache"`（dto/adapter 已在 imports）。

(b) `New` 中 metrics.Init 块之后、`s.router = s.buildRouter()` 之前插入：

```go
	// Tiered read caches (issue #113). Disabled by default; nil caches make
	// every handler pass through exactly as before.
	if err := s.initCaches(); err != nil {
		return nil, fmt.Errorf("initializing tiered cache: %w", err)
	}
```

(c) `Stop` 中 `s.stopWatcher()` 之后插入：

```go
	if s.cacheCancel != nil {
		s.cacheCancel()
		<-s.cacheDone
	}
```

(d) `buildRouter` 路由表替换（root 与 legacy 两组都改）：

```go
	r.Get("/readyz", v1.ReadyHandlerWithCache(s.registry, s.healthCache))
	// sandboxes（root :379-384 与 legacy :495-500）
	r.Post("/sandboxes", sandboxWrite(v1.CreateSandboxHandlerWithCache(s.registry, s.routeMgr, s.cfg.Server.EnvdDomain, s.sandboxCache)))
	r.Get("/sandboxes/{sandboxID}", sandboxRead(v1.GetSandboxHandlerWithCache(s.registry, s.routeMgr, s.sandboxCache)))
	r.Delete("/sandboxes/{sandboxID}", sandboxWrite(v1.KillSandboxHandlerWithCache(s.registry, s.routeMgr, s.sandboxCache)))
	r.Post("/sandboxes/{sandboxID}/pause", sandboxWrite(v1.PauseSandboxHandlerWithCache(s.registry, s.routeMgr, s.sandboxCache)))
	r.Post("/sandboxes/{sandboxID}/resume", sandboxWrite(v1.ResumeSandboxHandlerWithCache(s.registry, s.routeMgr, s.sandboxCache)))
	// templates（:439-442、:479-481、:539-542）
	r.Get("/templates", templateRead(v1.ListTemplatesHandlerWithCache(s.registry, s.routeMgr, s.templateCache)))
	r.Post("/templates", templateWrite(v1.CreateTemplateHandlerWithCache(s.registry, s.routeMgr, s.templateCache)))
	r.Delete("/templates/{templateID}", templateWrite(v1.DeleteTemplateHandlerWithCache(s.registry, s.routeMgr, s.templateCache)))
	r.Get("/templates", templateRead(v1.ListTemplatesHandlerV2WithCache(s.registry, s.routeMgr, s.templateCache)))      // v2 组
	r.Post("/templates", templateWrite(v1.CreateTemplateHandlerV2WithCache(s.registry, s.routeMgr, s.templateCache)))   // v2 组
	r.Patch("/templates/{templateID}", templateWrite(v1.UpdateTemplateHandlerWithCache(s.registry, s.routeMgr, s.templateCache))) // v2 组
```

同时用 grep 找到 `SetTimeoutHandler` 的路由行（`grep -n "SetTimeoutHandler" internal/server/http.go`），同样换成 `SetTimeoutHandlerWithCache(..., s.sandboxCache)`。

- [ ] **Step 5: 跑测试确认通过**

Run: `go test -race ./internal/server/ ./internal/api/v1/ -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/server/cache.go internal/server/cache_test.go internal/server/http.go
git commit -s -m "feat(server): wire tiered caches and invalidation subscriber (#113)"
```

---

### Task 9: 双副本一致性集成测试（进程内，真实 handler）

**Files:**
- Create: `test/integration/tiered_cache_test.go`

**Interfaces:**
- Consumes: Task 3/4/5/6 全部公开 API；`mockadapter.New(nil)`（注册名 "mock"，见 handlers_test.go:55）；`adapter.NewRegistry(sessionstore.NewMemory(), time.Hour)`；`routing.NewRouter(config.RoutingConfig{DefaultBackend:"mock", Strategy:"static"}, reg)`
- Produces: 无（纯测试）。验证 issue 验收标准：2 副本变更 1s 内可见。

- [ ] **Step 1: 写测试**

`test/integration/tiered_cache_test.go`（package integration，license header）：

```go
package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	mockadapter "github.com/e2bgateway/e2bgateway/internal/adapter/mock"
	"github.com/e2bgateway/e2bgateway/internal/api/dto"
	v1 "github.com/e2bgateway/e2bgateway/internal/api/v1"
	"github.com/e2bgateway/e2bgateway/internal/cache"
	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/routing"
	"github.com/e2bgateway/e2bgateway/internal/sessionstore"
)

// cacheReplica models one gateway pod: its own L1 + invalidator, shared
// Redis L2, and the SAME backend registry (both pods talk to one backend).
type cacheReplica struct {
	sbx  *cache.TieredCache[dto.SandboxInfo]
	tmpl *cache.TieredCache[[]*adapter.Template]
}

func newCacheReplica(t *testing.T, mr *miniredis.Miniredis) cacheReplica {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	inv := cache.NewInvalidator(client, "test:invalidate")
	mkOpts := func(name string) cache.TieredOptions {
		return cache.TieredOptions{
			Name: name, L1MaxSize: 100, L1TTL: time.Minute,
			L2:          cache.NewRedisCache(cache.WrapRedisClient(client), "test:", time.Minute),
			Invalidator: inv,
		}
	}
	r := cacheReplica{
		sbx:  cache.NewTiered[dto.SandboxInfo](mkOpts("sandbox_metadata")),
		tmpl: cache.NewTiered[[]*adapter.Template](mkOpts("template_list")),
	}
	inv.Register("sandbox_metadata", r.sbx.OnRemoteInvalidate)
	inv.Register("template_list", r.tmpl.OnRemoteInvalidate)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go inv.Run(ctx)
	select {
	case <-inv.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("invalidator subscriber not ready")
	}
	return r
}

func sandboxRequest(method, id string) *http.Request {
	req := httptest.NewRequest(method, "/sandboxes/"+id, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sandboxID", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// TestTieredCache_TwoReplicaSandboxConsistency: kill via replica A must be
// visible on replica B within 1s (issue #113 acceptance criterion).
func TestTieredCache_TwoReplicaSandboxConsistency(t *testing.T) {
	mr := miniredis.RunT(t)
	reg := adapter.NewRegistry(sessionstore.NewMemory(), time.Hour)
	require.NoError(t, reg.Register(mockadapter.New(nil)))
	router := routing.NewRouter(config.RoutingConfig{DefaultBackend: "mock", Strategy: "static"}, reg)
	a := newCacheReplica(t, mr)
	b := newCacheReplica(t, mr)

	// Create via replica A's handler (write-through).
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sandboxes", strings.NewReader(`{"templateID":"base","timeout":300}`))
	req.Header.Set("Content-Type", "application/json")
	v1.CreateSandboxHandlerWithCache(reg, router, "", a.sbx)(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	var created dto.SandboxCreateResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	id := created.SandboxID

	// Warm replica B's cache.
	w = httptest.NewRecorder()
	v1.GetSandboxHandlerWithCache(reg, router, b.sbx)(w, sandboxRequest(http.MethodGet, id))
	require.Equal(t, http.StatusOK, w.Code)

	// Kill via replica A.
	w = httptest.NewRecorder()
	v1.KillSandboxHandlerWithCache(reg, router, a.sbx)(w, sandboxRequest(http.MethodDelete, id))
	require.Equal(t, http.StatusNoContent, w.Code)

	// Replica B must see 404 within 1s.
	require.Eventually(t, func() bool {
		w := httptest.NewRecorder()
		v1.GetSandboxHandlerWithCache(reg, router, b.sbx)(w, sandboxRequest(http.MethodGet, id))
		return w.Code == http.StatusNotFound
	}, time.Second, 10*time.Millisecond)
}

// TestTieredCache_TwoReplicaTemplateConsistency: template created via A is
// listed by B within 1s.
func TestTieredCache_TwoReplicaTemplateConsistency(t *testing.T) {
	mr := miniredis.RunT(t)
	reg := adapter.NewRegistry(sessionstore.NewMemory(), time.Hour)
	require.NoError(t, reg.Register(mockadapter.New(nil)))
	router := routing.NewRouter(config.RoutingConfig{DefaultBackend: "mock", Strategy: "static"}, reg)
	a := newCacheReplica(t, mr)
	b := newCacheReplica(t, mr)

	listLen := func(r cacheReplica) int {
		w := httptest.NewRecorder()
		v1.ListTemplatesHandlerWithCache(reg, router, r.tmpl)(w, httptest.NewRequest(http.MethodGet, "/templates", nil))
		require.Equal(t, http.StatusOK, w.Code)
		var out []dto.TemplateInfo
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		return len(out)
	}
	before := listLen(b) // warm B

	// Create via replica A's handler.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/templates", strings.NewReader(`{"name":"two-replica-tmpl"}`))
	req.Header.Set("Content-Type", "application/json")
	v1.CreateTemplateHandlerWithCache(reg, router, a.tmpl)(w, req)
	require.Equal(t, http.StatusAccepted, w.Code)

	require.Eventually(t, func() bool { return listLen(b) == before+1 }, time.Second, 10*time.Millisecond)
	assert.Equal(t, before+1, listLen(a))
}
```

- [ ] **Step 2: 跑测试**

Run: `go test -race ./test/integration/ -run TestTieredCache -count=1`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add test/integration/tiered_cache_test.go
git commit -s -m "test(integration): two-replica tiered cache consistency (#113)"
```

---

### Task 10: E2E 双副本测试（env 门控）

**Files:**
- Create: `test/e2e/multi_replica_cache_test.go`

**Interfaces:**
- Consumes: 沿用 `test/e2e/multi_replica_test.go` 的 `GATEWAY_URL` / `GATEWAY_URL_B` / `E2B_API_KEY` 门控模式；要求部署开启 `cache.tiered` 且共享 Redis。
- Produces: 无（纯测试）。

- [ ] **Step 1: 写测试**

`test/e2e/multi_replica_cache_test.go`（package e2e，license header）：

```go
package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/e2bgateway/e2bgateway/internal/api/dto"
)

// TestMultiReplicaCacheInvalidation runs against TWO replicas sharing a
// Redis L2 (cache.tiered enabled on both). Proves issue #113 acceptance:
// a sandbox killed via replica A returns 404 on replica B within 1s.
func TestMultiReplicaCacheInvalidation(t *testing.T) {
	urlA := os.Getenv("GATEWAY_URL")
	urlB := os.Getenv("GATEWAY_URL_B")
	if urlA == "" || urlB == "" {
		t.Skip("GATEWAY_URL / GATEWAY_URL_B not set — multi-replica E2E only")
	}
	apiKey := os.Getenv("E2B_API_KEY")
	client := &http.Client{Timeout: 30 * time.Second}

	do := func(method, url, body string) (int, []byte) {
		var rdr io.Reader
		if body != "" {
			rdr = bytes.NewReader([]byte(body))
		}
		req, err := http.NewRequest(method, url, rdr)
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", apiKey)
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}

	// 1. Create via A.
	code, body := do(http.MethodPost, urlA+"/sandboxes", `{"templateID":"base","timeout":300}`)
	require.Contains(t, []int{http.StatusOK, http.StatusCreated}, code, "create: %s", body)
	var created dto.SandboxCreateResponse
	require.NoError(t, json.Unmarshal(body, &created))

	// 2. Warm B's cache.
	code, body = do(http.MethodGet, urlB+"/sandboxes/"+created.SandboxID, "")
	require.Equal(t, http.StatusOK, code, "warm B: %s", body)

	// 3. Kill via A.
	code, body = do(http.MethodDelete, urlA+"/sandboxes/"+created.SandboxID, "")
	require.Equal(t, http.StatusNoContent, code, "kill A: %s", body)

	// 4. B must return 404 within 1s (invalidation broadcast).
	require.Eventually(t, func() bool {
		code, _ := do(http.MethodGet, urlB+"/sandboxes/"+created.SandboxID, "")
		return code == http.StatusNotFound
	}, time.Second, 20*time.Millisecond)

	// 5. Cache metrics exist on B (hit-rate evidence for QPS reduction).
	code, body = do(http.MethodGet, urlB+"/metrics", "")
	require.Equal(t, http.StatusOK, code)
	for _, name := range []string{
		"e2bgw_cache_l1_hits_total",
		"e2bgw_cache_misses_total",
		"e2bgw_cache_invalidations_total",
	} {
		require.Contains(t, string(body), name)
	}
}
```

- [ ] **Step 2: 本地验证 skip 路径 + 编译**

Run: `go test ./test/e2e/ -run TestMultiReplicaCacheInvalidation -count=1 -v`
Expected: SKIP（env 未设）且编译通过。真实双副本运行在 kind-e2e 环境执行（`test/kind-e2e/` manifests 需开启 cache.tiered + 共享 redis；若现有 manifests 未配，属部署侧后续工作，不在本 PR 改 manifests）。

- [ ] **Step 3: Commit**

```bash
git add test/e2e/multi_replica_cache_test.go
git commit -s -m "test(e2e): multi-replica cache invalidation acceptance test (#113)"
```

---

### Task 11: 文档 + 全量校验

**Files:**
- Modify: `AGENTS.md`（key-packages 表中 `internal/cache/` 行，约 :200-228）
- Modify: `configs/e2bgateway-example.yaml`（`cache:` 块，:220）

**Interfaces:**
- Consumes: 全部前序 Task
- Produces: 文档一致性；`make pre-commit` 通过。

- [ ] **Step 1: 更新 AGENTS.md**

key-packages 表 `internal/cache/` 行描述改为：

```
Thread-safe LRU cache with TTL; TieredCache[T] (L1 memory + L2 Redis) with Pub/Sub cross-replica invalidation and L2 degradation
```

- [ ] **Step 2: 更新 example yaml**

`cache:` 块追加（注释示例，默认关闭）：

```yaml
cache:
  backend: memory
  memory:
    maxSize: 1000
    defaultTTL: 30s
  redis:
    address: "redis:6379"
    defaultTTL: 60s
  # Tiered read cache (issue #113): L1 in-memory + optional shared Redis L2
  # with Pub/Sub cross-replica invalidation. Disabled by default. Enabled
  # without l2.address runs L1-only (single replica). Cached payloads carry
  # no access tokens; TLS is still recommended for non-loopback Redis.
  # tiered:
  #   enabled: true
  #   l1:
  #     maxSize: 10000
  #     defaultTTL: 30s
  #   l2:
  #     address: "redis:6379"
  #     password: ""
  #     db: 0
  #     tls: false
  #     keyPrefix: "e2bgw:cache:"
  #     pubsubChannel: "e2bgw:cache:invalidate"
  #     timeout: 200ms
```

- [ ] **Step 3: 全量校验**

Run: `make pre-commit`
Expected: fmt + vet + lint + test 全部通过。若 lint 报 `gocyclo`/`errcheck` 等，按仓库既有 nolint 风格处理（参照 cache.go:148）。

- [ ] **Step 4: Commit**

```bash
git add AGENTS.md configs/e2bgateway-example.yaml
git commit -s -m "docs: document tiered cache configuration and package (#113)"
```

---

## Self-Review 结论（写计划时已执行）

1. **Spec 覆盖**：§3.1 组合器→Task 3；§3.1 Invalidator→Task 4；§3.3 降级→Task 3（l2Get/fill/warn）；§3.4 三条热点路径+失效触发→Task 5/6/7（含 spec 未列但元数据相关的 SetTimeout 失效）；§5 配置→Task 1；§6 安全→Task 1 validate + Task 8 TLS WARN；§7 metrics→Task 2；§8 测试→Task 3/4/9/10；§9 文档→Task 11。tenant config N/A 无任务（符合 spec non-goal）。
2. **占位符**：无（初稿中的占位函数已删除，测试代码完整）。
3. **类型一致性**：`TieredOptions.Invalidator`（Broadcaster）↔ `Invalidator.Broadcast(ctx, cacheName, key)` 签名一致；`OnRemoteInvalidate(key string)` ↔ `Register(name, func(string))` 一致；`Ready()` 在 Task 4 定义、Task 9 使用，一致。