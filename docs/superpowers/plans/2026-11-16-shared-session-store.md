# Shared Session Store (#90) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the gateway run with multiple replicas by persisting envd access tokens and the sandbox→backend routing map in a pluggable shared store (memory | redis), and prove it with a 2-replica kind E2E.

**Architecture:** New `internal/sessionstore` package defines a TTL-aware string KV `Store` (memory + go-redis implementations sharing a contract test suite) plus token helpers with SetNX arbitration. `server.New` builds the store once from a new top-level `sessionStore` config block, passes it to `adapter.NewRegistry` (routing map becomes store-backed) and injects it into agentsandbox/opensandbox adapters via a `sessionstore.Aware` interface (their in-process token LRU is deleted). The envd proxy's wrong-backend fallback on routing miss becomes 404 (multi-backend) / sole-adapter (single-backend).

**Tech Stack:** Go 1.26, `github.com/redis/go-redis/v9` (existing dep), `github.com/alicebob/miniredis/v2` (existing test dep), kind + GitHub Actions for E2E.

**Spec:** `docs/superpowers/specs/2026-11-16-shared-session-store-design.md`

## Global Constraints

- Branch `feat/shared-session-store-issue-90` (already checked out, based on `main`). Module `github.com/e2bgateway/e2bgateway`.
- Every new `.go` file starts with the repo Apache-2.0 license header (copy from `internal/auth/auth.go:1-13`).
- `golangci-lint` depguard **forbids `sort`** (use `slices`) — and check `.golangci.yml` before adding stdlib imports.
- No new Go dependencies (go-redis and miniredis are already in `go.mod`).
- Never log token values or store values; structured logs carry `sandboxID`, `backend`, `op` only (adapters use `log/slog`).
- Default behavior (`sessionStore.type: memory`, single replica) must be unchanged for existing deployments.
- TLS rule (CWE-319, existing precedent): redis password set + non-loopback addr ⇒ TLS required (`tls: true` or `rediss://` addr); no insecure-skip-verify.
- Verification gate: `make pre-commit`. Commit messages: conventional commits + `(#90)`; **all commits signed** (`git commit -s`, repo enforces DCO).
- Key layout: tokens at `{keyPrefix}token:{sandboxID}` (JSON value, TTL 1h), routing at `{keyPrefix}routing:{sandboxID}` (backend name, TTL `routingTTL` default 24h refreshed on read). Callers pass **logical** keys (`token:...`, `routing:...`); the store implementation prepends `keyPrefix`.

---

### Task 1: Extract `internal/redisx` shared TLS helpers

**Files:**
- Create: `internal/redisx/redisx.go` (license header + moved code, exported names)
- Create: `internal/redisx/redisx_test.go` (move the existing test file content)
- Delete: `internal/adapter/opensandbox/redis_tls.go`, `internal/adapter/opensandbox/redis_tls_test.go`
- Modify: `internal/adapter/opensandbox/redis_store.go:89-90`, `internal/adapter/opensandbox/warmpool.go:199-200,219-220` (call sites)

**Interfaces:**
- Produces (used by Task 2 config validation and Task 4 redis store):
  - `func redisx.NormalizeRedisAddr(addr string) (hostPort string, schemeWantsTLS bool)`
  - `func redisx.TLSConfig(addr string, tlsEnabled, hasPassword bool) (*tls.Config, error)`
  - `func redisx.IsLoopbackHost(host string) bool`

- [ ] **Step 1: Move the code**

`git mv internal/adapter/opensandbox/redis_tls.go internal/redisx/redisx.go` (create the dir first), then in `redisx.go`: change `package opensandbox` → `package redisx` (update the file's package clause only; keep license header), rename `normalizeRedisAddr` → `NormalizeRedisAddr`, `redisTLSConfig` → `TLSConfig`, `isLoopbackHost` → `IsLoopbackHost`, keep `redissPrefix` unexported. Update doc comments to say the package is shared by opensandbox (templateStore, warm-pool stateStore) and the session store. Same for the test file: `git mv internal/adapter/opensandbox/redis_tls_test.go internal/redisx/redisx_test.go`, change package clause to `redisx`, and rename the three function references inside (`normalizeRedisAddr`→`NormalizeRedisAddr`, `redisTLSConfig`→`TLSConfig`, `isLoopbackHost`→`IsLoopbackHost`).

- [ ] **Step 2: Update opensandbox call sites**

In `internal/adapter/opensandbox/redis_store.go` (lines 89-90):

```go
	addr, schemeTLS := redisx.NormalizeRedisAddr(cfg.Addr)
	tlsCfg, err := redisx.TLSConfig(addr, cfg.EnableTLS || schemeTLS, cfg.Password != "")
```

In `internal/adapter/opensandbox/warmpool.go` (lines 199-200):

```go
	addr, schemeTLS := redisx.NormalizeRedisAddr(cfg.Addr)
	if _, err := redisx.TLSConfig(addr, cfg.EnableTLS || schemeTLS, cfg.Password != ""); err != nil {
```

and (lines 219-220):

```go
	addr, schemeTLS := redisx.NormalizeRedisAddr(a.poolStoreCfg.Addr)
	tlsCfg, err := redisx.TLSConfig(addr, a.poolStoreCfg.EnableTLS || schemeTLS, a.poolStoreCfg.Password != "")
```

Add `"github.com/e2bgateway/e2bgateway/internal/redisx"` to both files' import blocks (internal-group, alphabetical). Remove any now-unused imports.

- [ ] **Step 3: Verify**

Run: `go build ./... && go test ./internal/redisx/ ./internal/adapter/opensandbox/ 2>&1 | tail -5`
Expected: build clean; both packages PASS.
Run: `grep -rn "normalizeRedisAddr\|redisTLSConfig" internal/adapter/opensandbox/ --include="*.go"`
Expected: no output (all call sites migrated).

- [ ] **Step 4: Commit**

```bash
git add -A internal/redisx internal/adapter/opensandbox
git commit -s -m "refactor(opensandbox): extract redis TLS helpers to internal/redisx (#90)"
```

---

### Task 2: `SessionStoreConfig` in internal/config

**Files:**
- Modify: `internal/config/config.go` — `Config` struct (line ~26), new type after `CacheConfig` (~line 164), `setDefaults`, `Validate` (line ~249)
- Test: `internal/config/config_test.go` (append)

**Interfaces:**
- Consumes: `redisx.TLSConfig` / `redisx.NormalizeRedisAddr` (Task 1).
- Produces:
```go
type SessionStoreConfig struct {
	Type       string        `mapstructure:"type"`      // memory | redis
	Addr       string        `mapstructure:"addr"`
	Password   string        `mapstructure:"password"`
	DB         int           `mapstructure:"db"`
	KeyPrefix  string        `mapstructure:"keyPrefix"`
	EnableTLS  bool          `mapstructure:"tls"`
	RoutingTTL time.Duration `mapstructure:"routingTTL"`
}
```
`Config.SessionStore SessionStoreConfig \`mapstructure:"sessionStore"\``. After `Validate()`: defaults normalized in-place — `Type` defaults `"memory"`, `KeyPrefix` defaults `"e2bgw:session:"`, `RoutingTTL` defaults `24h` when `<= 0`. Tasks 4/6 rely on these normalized values.

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:

```go
func TestValidateSessionStore(t *testing.T) {
	base := &Config{
		Server:   ServerConfig{HTTP: HTTPConfig{Address: "0.0.0.0:8080"}},
		Backends: []BackendConfig{{Name: "mock", Type: "mock", Enabled: true}},
	}

	// Defaults normalize in place.
	cfg := *base
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error: %v", err)
	}
	if cfg.SessionStore.Type != "memory" {
		t.Errorf("expected default type 'memory', got %q", cfg.SessionStore.Type)
	}
	if cfg.SessionStore.KeyPrefix != "e2bgw:session:" {
		t.Errorf("expected default keyPrefix, got %q", cfg.SessionStore.KeyPrefix)
	}
	if cfg.SessionStore.RoutingTTL != 24*time.Hour {
		t.Errorf("expected default routingTTL 24h, got %v", cfg.SessionStore.RoutingTTL)
	}

	// Unknown type rejected.
	cfg = *base
	cfg.SessionStore = SessionStoreConfig{Type: "etcd"}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for unknown sessionStore type")
	}

	// redis without addr rejected.
	cfg = *base
	cfg.SessionStore = SessionStoreConfig{Type: "redis"}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for redis without addr")
	}

	// redis with negative db rejected.
	cfg = *base
	cfg.SessionStore = SessionStoreConfig{Type: "redis", Addr: "localhost:6379", DB: -1}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for negative db")
	}

	// password + non-loopback addr without TLS rejected (CWE-319 rule).
	cfg = *base
	cfg.SessionStore = SessionStoreConfig{Type: "redis", Addr: "redis.prod:6379", Password: "secret"}
	if err := cfg.Validate(); err == nil {
		t.Error("expected TLS enforcement error")
	}

	// same, but TLS enabled: accepted.
	cfg = *base
	cfg.SessionStore = SessionStoreConfig{Type: "redis", Addr: "redis.prod:6379", Password: "secret", EnableTLS: true}
	if err := cfg.Validate(); err != nil {
		t.Errorf("tls enabled: unexpected error: %v", err)
	}
}
```

(Add `"time"` to the test file's imports if not already present.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestValidateSessionStore -v 2>&1 | tail -3`
Expected: compile FAIL — `undefined: SessionStoreConfig`.

- [ ] **Step 3: Implement**

In `internal/config/config.go`:

3a. Add to the `Config` struct after `Cache`:

```go
	SessionStore  SessionStoreConfig  `mapstructure:"sessionStore"`
```

3b. Add the type (after `CacheConfig` / its sub-structs):

```go
// SessionStoreConfig defines the shared data-plane session store (envd access
// tokens + sandbox→backend routing). type: redis is required to run more than
// one gateway replica; type: memory (default) keeps per-process state and
// single-replica behavior.
type SessionStoreConfig struct {
	Type       string        `mapstructure:"type"` // memory | redis
	Addr       string        `mapstructure:"addr"`
	Password   string        `mapstructure:"password"`
	DB         int           `mapstructure:"db"`
	KeyPrefix  string        `mapstructure:"keyPrefix"`
	EnableTLS  bool          `mapstructure:"tls"`
	RoutingTTL time.Duration `mapstructure:"routingTTL"`
}
```

3c. In `setDefaults(v)` add:

```go
	v.SetDefault("sessionStore.type", "memory")
	v.SetDefault("sessionStore.keyPrefix", "e2bgw:session:")
	v.SetDefault("sessionStore.routingTTL", "24h")
```

3d. In `Config.Validate()`, after the auth-providers loop, add normalization + validation:

```go
	if err := c.SessionStore.validate(); err != nil {
		return err
	}
```

and the method:

```go
// validate normalizes defaults in place and rejects unusable session-store
// configurations before the server starts.
func (c *SessionStoreConfig) validate() error {
	switch c.Type {
	case "":
		c.Type = "memory"
	case "memory":
	case "redis":
		if c.Addr == "" {
			return fmt.Errorf("sessionStore: addr is required when type is redis")
		}
		if c.DB < 0 {
			return fmt.Errorf("sessionStore: db must be >= 0")
		}
		addr, schemeTLS := redisx.NormalizeRedisAddr(c.Addr)
		if _, err := redisx.TLSConfig(addr, c.EnableTLS || schemeTLS, c.Password != ""); err != nil {
			return fmt.Errorf("sessionStore: %w", err)
		}
	default:
		return fmt.Errorf("sessionStore: unsupported type %q (want memory or redis)", c.Type)
	}
	if c.KeyPrefix == "" {
		c.KeyPrefix = "e2bgw:session:"
	}
	if c.RoutingTTL <= 0 {
		c.RoutingTTL = 24 * time.Hour
	}
	return nil
}
```

Add `"github.com/e2bgateway/e2bgateway/internal/redisx"` to config.go's imports.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/config/ -v 2>&1 | tail -5`
Expected: PASS (all config tests).

- [ ] **Step 5: Commit**

```bash
git add internal/config/
git commit -s -m "feat(config): add sessionStore configuration block (#90)"
```

---

### Task 3: `internal/sessionstore` — Store interface + memory implementation + contract suite

**Files:**
- Create: `internal/sessionstore/store.go` (interface + key helpers)
- Create: `internal/sessionstore/memory.go`
- Create: `internal/sessionstore/contract_test.go` (shared suite, run against memory now; Task 4 plugs redis in)
- Create: `internal/sessionstore/memory_test.go` (thin: runs the contract against memory)

**Interfaces:**
- Produces (used by Tasks 4-9):
```go
type Store interface {
	Get(ctx context.Context, key string) (value string, ok bool, err error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error)
	Delete(ctx context.Context, key string) error
	Scan(ctx context.Context, prefix string, visit func(key, value string)) error
}
func NewMemory() Store
```
  Semantics: `ttl <= 0` = no expiry. `Get` on expired/missing key → `("", false, nil)`. `SetNX` writes only when the key is absent **or expired**, returning whether it wrote. `Scan` visits live (unexpired) keys whose logical key starts with `prefix`; `visit` returning is not relied on for control flow.

- [ ] **Step 1: Write store.go**

```go
package sessionstore

import (
	"context"
	"time"
)

// Store is a TTL-aware string key-value store shared across gateway
// replicas. The memory implementation keeps per-process state (single
// replica); the redis implementation shares state cluster-wide. Keys are
// logical ("token:{id}", "routing:{id}"); implementations prepend any
// configured key prefix. Error returns are always real failures — a missing
// or expired key is (zero, false, nil), never an error.
type Store interface {
	Get(ctx context.Context, key string) (value string, ok bool, err error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error)
	Delete(ctx context.Context, key string) error
	Scan(ctx context.Context, prefix string, visit func(key, value string)) error
}
```

- [ ] **Step 2: Write the contract suite (fails to compile — NewMemory undefined)**

`internal/sessionstore/contract_test.go`:

```go
package sessionstore

import (
	"context"
	"testing"
	"time"
)

// storeFixture is one contract-suite run target. advance makes TTL expiry
// deterministic: memory tests sleep, miniredis tests FastForward.
type storeFixture struct {
	store   Store
	advance func(t *testing.T, d time.Duration)
}

// runStoreContract is the shared behavioral suite every Store implementation
// must pass.
func runStoreContract(t *testing.T, fx storeFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("set get roundtrip", func(t *testing.T) {
		if err := fx.store.Set(ctx, "k1", "v1", time.Minute); err != nil {
			t.Fatal(err)
		}
		v, ok, err := fx.store.Get(ctx, "k1")
		if err != nil || !ok || v != "v1" {
			t.Fatalf("got (%q,%v,%v), want (v1,true,nil)", v, ok, err)
		}
	})

	t.Run("get miss", func(t *testing.T) {
		v, ok, err := fx.store.Get(ctx, "absent")
		if err != nil || ok || v != "" {
			t.Fatalf("got (%q,%v,%v), want (\"\",false,nil)", v, ok, err)
		}
	})

	t.Run("delete", func(t *testing.T) {
		if err := fx.store.Set(ctx, "k2", "v2", time.Minute); err != nil {
			t.Fatal(err)
		}
		if err := fx.store.Delete(ctx, "k2"); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := fx.store.Get(ctx, "k2"); ok {
			t.Fatal("key still present after delete")
		}
		// deleting a missing key is not an error
		if err := fx.store.Delete(ctx, "k2"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("ttl expiry", func(t *testing.T) {
		if err := fx.store.Set(ctx, "k3", "v3", 100*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		fx.advance(t, 200*time.Millisecond)
		if _, ok, _ := fx.store.Get(ctx, "k3"); ok {
			t.Fatal("expired key still visible")
		}
	})

	t.Run("no expiry when ttl <= 0", func(t *testing.T) {
		if err := fx.store.Set(ctx, "k4", "v4", 0); err != nil {
			t.Fatal(err)
		}
		fx.advance(t, time.Second)
		if v, ok, _ := fx.store.Get(ctx, "k4"); !ok || v != "v4" {
			t.Fatal("ttl<=0 key must not expire")
		}
	})

	t.Run("setnx arbitration", func(t *testing.T) {
		won, err := fx.store.SetNX(ctx, "k5", "first", time.Minute)
		if err != nil || !won {
			t.Fatalf("first SetNX: (%v,%v), want (true,nil)", won, err)
		}
		won, err = fx.store.SetNX(ctx, "k5", "second", time.Minute)
		if err != nil || won {
			t.Fatalf("second SetNX: (%v,%v), want (false,nil)", won, err)
		}
		if v, _, _ := fx.store.Get(ctx, "k5"); v != "first" {
			t.Fatalf("SetNX loser overwrote value: %q", v)
		}
	})

	t.Run("setnx after expiry wins", func(t *testing.T) {
		if _, err := fx.store.SetNX(ctx, "k6", "old", 100*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		fx.advance(t, 200*time.Millisecond)
		won, err := fx.store.SetNX(ctx, "k6", "new", time.Minute)
		if err != nil || !won {
			t.Fatalf("SetNX after expiry: (%v,%v), want (true,nil)", won, err)
		}
	})

	t.Run("scan visits prefix only", func(t *testing.T) {
		for k, v := range map[string]string{"p:a": "1", "p:b": "2", "q:c": "3"} {
			if err := fx.store.Set(ctx, k, v, time.Minute); err != nil {
				t.Fatal(err)
			}
		}
		seen := map[string]string{}
		if err := fx.store.Scan(ctx, "p:", func(k, v string) { seen[k] = v }); err != nil {
			t.Fatal(err)
		}
		if len(seen) != 2 || seen["p:a"] != "1" || seen["p:b"] != "2" {
			t.Fatalf("scan result: %v", seen)
		}
	})
}
```

`internal/sessionstore/memory_test.go`:

```go
package sessionstore

import (
	"testing"
	"time"
)

func TestMemoryStoreContract(t *testing.T) {
	runStoreContract(t, storeFixture{
		store:   NewMemory(),
		advance: func(t *testing.T, d time.Duration) { time.Sleep(d) },
	})
}
```

- [ ] **Step 3: Run to verify compile failure**

Run: `go test ./internal/sessionstore/ 2>&1 | tail -3`
Expected: FAIL — `undefined: NewMemory`.

- [ ] **Step 4: Implement memory.go**

```go
package sessionstore

import (
	"context"
	"strings"
	"sync"
	"time"
)

type memoryEntry struct {
	value     string
	expiresAt time.Time // zero value = no expiry
}

func (e memoryEntry) expired(now time.Time) bool {
	return !e.expiresAt.IsZero() && now.After(e.expiresAt)
}

// memoryStore is the per-process Store implementation. Expiry is lazy:
// entries are dropped when touched by Get/SetNX/Delete/Scan.
type memoryStore struct {
	mu      sync.RWMutex
	entries map[string]memoryEntry
}

// NewMemory returns a Store backed by an in-process map. Suitable for
// single-replica deployments and tests.
func NewMemory() Store {
	return &memoryStore{entries: make(map[string]memoryEntry)}
}

func expiry(ttl time.Duration) time.Time {
	if ttl <= 0 {
		return time.Time{}
	}
	return time.Now().Add(ttl)
}

func (m *memoryStore) Get(_ context.Context, key string) (string, bool, error) {
	m.mu.RLock()
	e, ok := m.entries[key]
	m.mu.RUnlock()
	if !ok || e.expired(time.Now()) {
		return "", false, nil
	}
	return e.value, true, nil
}

func (m *memoryStore) Set(_ context.Context, key, value string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = memoryEntry{value: value, expiresAt: expiry(ttl)}
	return nil
}

func (m *memoryStore) SetNX(_ context.Context, key, value string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[key]; ok && !e.expired(time.Now()) {
		return false, nil
	}
	m.entries[key] = memoryEntry{value: value, expiresAt: expiry(ttl)}
	return true, nil
}

func (m *memoryStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, key)
	return nil
}

func (m *memoryStore) Scan(_ context.Context, prefix string, visit func(key, value string)) error {
	now := time.Now()
	m.mu.RLock()
	defer m.mu.RUnlock()
	for k, e := range m.entries {
		if strings.HasPrefix(k, prefix) && !e.expired(now) {
			visit(k, e.value)
		}
	}
	return nil
}
```

(Add the license header to every new file in this package.)

- [ ] **Step 5: Run tests**

Run: `go test ./internal/sessionstore/ -v 2>&1 | tail -12`
Expected: PASS — all contract subtests green against memory.

- [ ] **Step 6: Commit**

```bash
git add internal/sessionstore/
git commit -s -m "feat(sessionstore): Store interface with in-memory implementation (#90)"
```

---

### Task 4: redis implementation + `New(cfg)` constructor

**Files:**
- Create: `internal/sessionstore/redis.go`
- Create: `internal/sessionstore/redis_test.go` (miniredis contract run + error cases)
- Modify: `internal/sessionstore/store.go` (add `New`)

**Interfaces:**
- Consumes: `config.SessionStoreConfig` (Task 2, validated/normalized), `redisx.NormalizeRedisAddr`/`redisx.TLSConfig` (Task 1), contract suite (Task 3).
- Produces:
  - `func New(cfg config.SessionStoreConfig) (Store, error)` — memory ⇒ `NewMemory()`; redis ⇒ client build + `Ping` (fail fast on unreachable).
  - `func NewRedisWithClient(client redis.UniversalClient, keyPrefix string) Store` — test seam mirroring `NewRedisTemplateStoreWithClient` precedent.
  - Import note: this makes `internal/sessionstore` import `internal/config`. `internal/config` must therefore NOT import sessionstore (it doesn't — it uses redisx only). `internal/adapter` importing sessionstore (Task 6) is fine: adapter→sessionstore→config, no cycle.

- [ ] **Step 1: Write redis_test.go (fails to compile)**

```go
package sessionstore

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/e2bgateway/e2bgateway/internal/config"
)

func miniredisFixture(t *testing.T) (storeFixture, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return storeFixture{
		store:   NewRedisWithClient(client, "test:"),
		advance: func(t *testing.T, d time.Duration) { mr.FastForward(d) },
	}, mr
}

func TestRedisStoreContract(t *testing.T) {
	fx, _ := miniredisFixture(t)
	runStoreContract(t, fx)
}

func TestRedisStoreKeyPrefix(t *testing.T) {
	fx, mr := miniredisFixture(t)
	ctx := context.Background()
	if err := fx.store.Set(ctx, "token:sb1", "v", time.Minute); err != nil {
		t.Fatal(err)
	}
	if !mr.Exists("test:token:sb1") {
		t.Error("expected physical key to carry the configured prefix")
	}
}

func TestRedisStoreErrors(t *testing.T) {
	fx, mr := miniredisFixture(t)
	mr.Close()
	ctx := context.Background()
	if _, _, err := fx.store.Get(ctx, "x"); err == nil {
		t.Error("expected error from closed redis")
	}
	if err := fx.store.Set(ctx, "x", "v", time.Minute); err == nil {
		t.Error("expected error from closed redis")
	}
}

func TestNewMemoryDispatch(t *testing.T) {
	s, err := New(config.SessionStoreConfig{Type: "memory"})
	if err != nil || s == nil {
		t.Fatalf("New(memory): (%v, %v)", s, err)
	}
}

func TestNewRedisRequiresReachableServer(t *testing.T) {
	// Port 1 on loopback: connection refused ⇒ New must fail (fail fast).
	_, err := New(config.SessionStoreConfig{Type: "redis", Addr: "127.0.0.1:1", KeyPrefix: "p:"})
	if err == nil {
		t.Fatal("expected error for unreachable redis")
	}
}
```

- [ ] **Step 2: Run to verify compile failure**

Run: `go test ./internal/sessionstore/ -run Redis 2>&1 | tail -3`
Expected: FAIL — `undefined: NewRedisWithClient`, `undefined: New`.

- [ ] **Step 3: Implement redis.go + New**

```go
package sessionstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/redisx"
)

// redisStore is the cluster-shared Store implementation backed by go-redis.
type redisStore struct {
	client redis.UniversalClient
	prefix string
}

// New builds the configured Store. For redis it verifies connectivity with a
// Ping so misconfiguration fails startup instead of every request.
func New(cfg config.SessionStoreConfig) (Store, error) {
	switch cfg.Type {
	case "", "memory":
		return NewMemory(), nil
	case "redis":
		addr, schemeTLS := redisx.NormalizeRedisAddr(cfg.Addr)
		tlsCfg, err := redisx.TLSConfig(addr, cfg.EnableTLS || schemeTLS, cfg.Password != "")
		if err != nil {
			return nil, fmt.Errorf("session store redis TLS config: %w", err)
		}
		client := redis.NewClient(&redis.Options{
			Addr:      addr,
			Password:  cfg.Password,
			DB:        cfg.DB,
			TLSConfig: tlsCfg,
		})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Ping(ctx).Err(); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("session store redis unreachable at %s: %w", cfg.Addr, err)
		}
		return &redisStore{client: client, prefix: cfg.KeyPrefix}, nil
	default:
		return nil, fmt.Errorf("session store: unsupported type %q", cfg.Type)
	}
}

// NewRedisWithClient builds a redisStore around an existing client (test seam).
func NewRedisWithClient(client redis.UniversalClient, keyPrefix string) Store {
	return &redisStore{client: client, prefix: keyPrefix}
}

func (r *redisStore) key(k string) string { return r.prefix + k }

func (r *redisStore) Get(ctx context.Context, key string) (string, bool, error) {
	v, err := r.client.Get(ctx, r.key(key)).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("session store get: %w", err)
	}
	return v, true, nil
}

func (r *redisStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	// go-redis: expiration 0 means "no expiry".
	if err := r.client.Set(ctx, r.key(key), value, maxTTL0(ttl)).Err(); err != nil {
		return fmt.Errorf("session store set: %w", err)
	}
	return nil
}

func (r *redisStore) SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	ok, err := r.client.SetNX(ctx, r.key(key), value, maxTTL0(ttl)).Result()
	if err != nil {
		return false, fmt.Errorf("session store setnx: %w", err)
	}
	return ok, nil
}

func (r *redisStore) Delete(ctx context.Context, key string) error {
	if err := r.client.Del(ctx, r.key(key)).Err(); err != nil {
		return fmt.Errorf("session store delete: %w", err)
	}
	return nil
}

func (r *redisStore) Scan(ctx context.Context, prefix string, visit func(key, value string)) error {
	pattern := r.key(prefix) + "*"
	iter := r.client.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		physKey := iter.Val()
		v, err := r.client.Get(ctx, physKey).Result()
		if errors.Is(err, redis.Nil) {
			continue // expired between SCAN and GET
		}
		if err != nil {
			return fmt.Errorf("session store scan: %w", err)
		}
		visit(strings.TrimPrefix(physKey, r.prefix), v)
	}
	return iter.Err()
}

// maxTTL0 maps ttl<=0 to go-redis' 0 ("persist").
func maxTTL0(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return 0
	}
	return ttl
}
```

(Add the license header. If lint rejects the helper name `maxTTL0`, rename to `persistZero` — behavior identical.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/sessionstore/ -v 2>&1 | tail -8`
Expected: PASS — memory AND redis contract runs, prefix, error, and dispatch tests.

- [ ] **Step 5: Commit**

```bash
git add internal/sessionstore/
git commit -s -m "feat(sessionstore): redis implementation with fail-fast startup ping (#90)"
```

---

### Task 5: Token helpers (`internal/sessionstore/token.go`)

**Files:**
- Create: `internal/sessionstore/token.go`
- Create: `internal/sessionstore/token_test.go`

**Interfaces:**
- Consumes: `Store` (Tasks 3-4).
- Produces (used by Tasks 8/9):
```go
func TokenKey(sandboxID string) string // "token:" + sandboxID
// GetToken returns the stored token and its real expiry.
func GetToken(ctx context.Context, s Store, sandboxID string) (token string, expiresAt time.Time, ok bool, err error)
// StoreTokenNX writes iff absent; won=false means another replica's token is live.
func StoreTokenNX(ctx context.Context, s Store, sandboxID, token string, expiresAt time.Time, ttl time.Duration) (won bool, err error)
// OverwriteToken unconditionally replaces the entry (race fallback).
func OverwriteToken(ctx context.Context, s Store, sandboxID, token string, expiresAt time.Time, ttl time.Duration) error
// ValidateToken reports whether token matches the stored entry; store errors propagate.
func ValidateToken(ctx context.Context, s Store, sandboxID, token string) (bool, error)
```
Wire format: JSON `{"token":"...","expiresAt":"<RFC3339>"}` under `TokenKey(sandboxID)`.

- [ ] **Step 1: Write token_test.go (fails to compile)**

```go
package sessionstore

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestTokenRoundTripAndExpiry(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	exp := time.Now().Add(time.Hour).Truncate(time.Second)

	if _, _, ok, err := GetToken(ctx, s, "sb1"); err != nil || ok {
		t.Fatalf("empty store: (%v,%v)", ok, err)
	}
	won, err := StoreTokenNX(ctx, s, "sb1", "tok-1", exp, time.Hour)
	if err != nil || !won {
		t.Fatalf("StoreTokenNX: (%v,%v)", won, err)
	}

	got, gotExp, ok, err := GetToken(ctx, s, "sb1")
	if err != nil || !ok || got != "tok-1" {
		t.Fatalf("GetToken: (%q,%v,%v)", got, ok, err)
	}
	if !gotExp.Equal(exp) {
		t.Errorf("expiresAt: got %v, want %v (stored expiry must survive round-trip)", gotExp, exp)
	}

	valid, err := ValidateToken(ctx, s, "sb1", "tok-1")
	if err != nil || !valid {
		t.Fatalf("ValidateToken(correct): (%v,%v)", valid, err)
	}
	valid, err = ValidateToken(ctx, s, "sb1", "wrong")
	if err != nil || valid {
		t.Fatalf("ValidateToken(wrong): (%v,%v)", valid, err)
	}
	valid, err = ValidateToken(ctx, s, "unknown-sandbox", "tok-1")
	if err != nil || valid {
		t.Fatalf("ValidateToken(missing sandbox): (%v,%v)", valid, err)
	}
}

// Two "replicas" racing GetOrCreate-style minting must converge on ONE token.
func TestTokenNXArbitrationConcurrent(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	const n = 16
	var wg sync.WaitGroup
	results := make([]string, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			tok, _, ok, err := GetToken(ctx, s, "sb-race")
			if err != nil {
				t.Errorf("replica %d GetToken: %v", i, err)
				return
			}
			if !ok {
				candidate := fmtToken(i)
				won, err := StoreTokenNX(ctx, s, "sb-race", candidate, time.Now().Add(time.Hour), time.Hour)
				if err != nil {
					t.Errorf("replica %d StoreTokenNX: %v", i, err)
					return
				}
				if !won {
					tok, _, _, err = GetToken(ctx, s, "sb-race")
					if err != nil {
						t.Errorf("replica %d re-GetToken: %v", i, err)
						return
					}
				} else {
					tok = candidate
				}
			}
			results[i] = tok
		}(i)
	}
	wg.Wait()
	for i, r := range results {
		if r != results[0] {
			t.Fatalf("replica %d got token %q, replica 0 got %q — arbitration broken", i, r, results[0])
		}
	}
}

func fmtToken(i int) string { return "tok-" + string(rune('a'+i)) }

func TestOverwriteToken(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	exp := time.Now().Add(time.Hour)
	if err := OverwriteToken(ctx, s, "sb2", "tok-old", exp, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := OverwriteToken(ctx, s, "sb2", "tok-new", exp, time.Hour); err != nil {
		t.Fatal(err)
	}
	got, _, _, err := GetToken(ctx, s, "sb2")
	if err != nil || got != "tok-new" {
		t.Fatalf("after overwrite: (%q,%v)", got, err)
	}
}
```

- [ ] **Step 2: Run to verify compile failure**

Run: `go test ./internal/sessionstore/ -run Token 2>&1 | tail -3`
Expected: FAIL — `undefined: GetToken` etc.

- [ ] **Step 3: Implement token.go**

```go
package sessionstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// tokenEntry is the JSON value stored under TokenKey. expiresAt is stored
// explicitly so callers report the token's REAL expiry instead of
// re-stamping now+TTL on every lookup.
type tokenEntry struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// TokenKey is the logical store key for a sandbox's envd access token.
func TokenKey(sandboxID string) string { return "token:" + sandboxID }

// GetToken returns the stored token for sandboxID, if any.
func GetToken(ctx context.Context, s Store, sandboxID string) (string, time.Time, bool, error) {
	raw, ok, err := s.Get(ctx, TokenKey(sandboxID))
	if err != nil || !ok {
		return "", time.Time{}, false, err
	}
	var e tokenEntry
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return "", time.Time{}, false, fmt.Errorf("decoding stored token entry: %w", err)
	}
	return e.Token, e.ExpiresAt, true, nil
}

// StoreTokenNX writes the token only if none is live; won=false means
// another replica already minted one — callers must GetToken and return
// the winner's value so one sandbox has exactly one live token.
func StoreTokenNX(ctx context.Context, s Store, sandboxID, token string, expiresAt time.Time, ttl time.Duration) (bool, error) {
	raw, err := json.Marshal(tokenEntry{Token: token, ExpiresAt: expiresAt})
	if err != nil {
		return false, fmt.Errorf("encoding token entry: %w", err)
	}
	return s.SetNX(ctx, TokenKey(sandboxID), string(raw), ttl)
}

// OverwriteToken unconditionally replaces the token entry. Fallback for the
// rare race where the winner's entry expires between a lost SetNX and the
// follow-up Get.
func OverwriteToken(ctx context.Context, s Store, sandboxID, token string, expiresAt time.Time, ttl time.Duration) error {
	raw, err := json.Marshal(tokenEntry{Token: token, ExpiresAt: expiresAt})
	if err != nil {
		return fmt.Errorf("encoding token entry: %w", err)
	}
	return s.Set(ctx, TokenKey(sandboxID), string(raw), ttl)
}

// ValidateToken reports whether token matches the live stored entry.
// A missing entry is (false, nil); store failures propagate as errors so
// callers can answer 502 rather than 401 during outages.
func ValidateToken(ctx context.Context, s Store, sandboxID, token string) (bool, error) {
	stored, _, ok, err := GetToken(ctx, s, sandboxID)
	if err != nil || !ok {
		return false, err
	}
	return stored == token, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/sessionstore/ -race -v 2>&1 | tail -8`
Expected: PASS including `TestTokenNXArbitrationConcurrent` under `-race`.

- [ ] **Step 5: Commit**

```bash
git add internal/sessionstore/
git commit -s -m "feat(sessionstore): token entry helpers with SetNX arbitration (#90)"
```

---

### Task 6: Store-backed `SandboxBackendMap` + registry/server wiring + all call sites

**Files:**
- Modify: `internal/adapter/interface.go:420-490` (SandboxBackendMap, NewRegistry)
- Modify: `internal/server/http.go` (Server struct ~line 45, New ~line 51-58)
- Modify call sites: `internal/adapter/agentsandbox/adapter.go:318,425`; `internal/adapter/opensandbox/adapter.go:393,455,518`; `internal/adapter/e2bcloud/adapter.go:116,124,169`; `internal/adapter/mock/adapter.go:121,165`; `internal/server/envd_proxy.go:77-83`
- Test: existing registry tests (grep `NewRegistry(` in `*_test.go` — update every construction), plus new `internal/adapter/registry_store_test.go`

**Interfaces:**
- Consumes: `sessionstore.Store`, `sessionstore.New(cfg)`, `config.SessionStoreConfig` (Tasks 2-4).
- Produces (used by Tasks 7-9 and all adapters):
```go
func NewSandboxBackendMap(store sessionstore.Store, routingTTL time.Duration) *SandboxBackendMap
func (m *SandboxBackendMap) Set(sandboxID, backendName string) error
func (m *SandboxBackendMap) Get(sandboxID string) (backendName string, ok bool, err error)
func (m *SandboxBackendMap) Delete(sandboxID string) error
func (m *SandboxBackendMap) Size() int                      // best effort (Scan count)
func (m *SandboxBackendMap) CountByBackend() map[string]int // best effort (Scan)
func NewRegistry(store sessionstore.Store, routingTTL time.Duration) *Registry
```
`Server` gains field `sessionStore sessionstore.Store` (exported to nothing; used by Task 8 injection).

- [ ] **Step 1: Write the failing registry store test**

Create `internal/adapter/registry_store_test.go` (license header, `package adapter`):

```go
package adapter

import (
	"testing"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/sessionstore"
)

func TestSandboxBackendMapStoreBacked(t *testing.T) {
	store := sessionstore.NewMemory()
	m := NewSandboxBackendMap(store, time.Hour)

	if err := m.Set("sb1", "agentsandbox"); err != nil {
		t.Fatal(err)
	}
	name, ok, err := m.Get("sb1")
	if err != nil || !ok || name != "agentsandbox" {
		t.Fatalf("Get: (%q,%v,%v)", name, ok, err)
	}

	// A second map over the same store sees the entry (cross-replica).
	m2 := NewSandboxBackendMap(store, time.Hour)
	if name, ok, _ := m2.Get("sb1"); !ok || name != "agentsandbox" {
		t.Fatalf("second replica Get: (%q,%v)", name, ok)
	}

	if _, ok, _ := m.Get("missing"); ok {
		t.Error("missing sandbox must not be found")
	}

	if err := m.Set("sb2", "opensandbox"); err != nil {
		t.Fatal(err)
	}
	counts := m.CountByBackend()
	if counts["agentsandbox"] != 1 || counts["opensandbox"] != 1 {
		t.Errorf("CountByBackend: %v", counts)
	}
	if m.Size() != 2 {
		t.Errorf("Size: %d, want 2", m.Size())
	}

	if err := m.Delete("sb1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := m.Get("sb1"); ok {
		t.Error("deleted sandbox still found")
	}
}
```

- [ ] **Step 2: Run to verify compile failure**

Run: `go test ./internal/adapter/ -run TestSandboxBackendMapStoreBacked 2>&1 | tail -3`
Expected: FAIL — too many arguments to `NewSandboxBackendMap` / signature mismatch.

- [ ] **Step 3: Rewrite SandboxBackendMap + NewRegistry**

In `internal/adapter/interface.go`, replace the whole `SandboxBackendMap` block (lines ~420-471, from the type comment through `CountByBackend`) with:

```go
// SandboxBackendMap maps sandbox IDs to their owning backend adapter names.
// It is populated by adapters when they create/get sandboxes and is used for
// routing in handlers and the envd proxy. Entries live in the shared session
// store so every replica (and every restart, with redis) sees the same map.
type SandboxBackendMap struct {
	store      sessionstore.Store
	routingTTL time.Duration
}

// routingKey is the logical session-store key for a sandbox's backend name.
func routingKey(sandboxID string) string { return "routing:" + sandboxID }

// NewSandboxBackendMap creates a store-backed sandbox-to-backend mapping.
func NewSandboxBackendMap(store sessionstore.Store, routingTTL time.Duration) *SandboxBackendMap {
	return &SandboxBackendMap{store: store, routingTTL: routingTTL}
}

// Set registers a sandbox ID with its owning backend adapter name.
// Fail-closed: callers on create paths must propagate the error — a sandbox
// whose routing entry cannot be recorded would be unreachable on other
// replicas.
func (m *SandboxBackendMap) Set(sandboxID, backendName string) error {
	return m.store.Set(context.Background(), routingKey(sandboxID), backendName, m.routingTTL)
}

// Get retrieves the backend adapter name for a sandbox ID, refreshing the
// entry's TTL so actively-used sandboxes never expire from the map.
func (m *SandboxBackendMap) Get(sandboxID string) (string, bool, error) {
	ctx := context.Background()
	name, ok, err := m.store.Get(ctx, routingKey(sandboxID))
	if err != nil || !ok {
		return "", false, err
	}
	// Refresh TTL; a failed refresh is not fatal — the entry is still valid.
	_ = m.store.Set(ctx, routingKey(sandboxID), name, m.routingTTL)
	return name, true, nil
}

// Delete removes a sandbox ID from the mapping (kill paths). Errors are
// returned for logging; the entry expires via TTL regardless.
func (m *SandboxBackendMap) Delete(sandboxID string) error {
	return m.store.Delete(context.Background(), routingKey(sandboxID))
}

// Size returns the number of tracked sandboxes (best effort on store errors).
func (m *SandboxBackendMap) Size() int {
	n := 0
	_ = m.store.Scan(context.Background(), "routing:", func(_, _ string) { n++ })
	return n
}

// CountByBackend returns the number of tracked sandboxes per backend name
// (best effort; consumed by metrics at scrape frequency).
func (m *SandboxBackendMap) CountByBackend() map[string]int {
	counts := make(map[string]int)
	_ = m.store.Scan(context.Background(), "routing:", func(_, backend string) { counts[backend]++ })
	return counts
}
```

Replace `NewRegistry`:

```go
// NewRegistry creates a new adapter registry whose sandbox→backend map is
// backed by the shared session store.
func NewRegistry(store sessionstore.Store, routingTTL time.Duration) *Registry {
	return &Registry{
		adapters:       make(map[string]SandboxAdapter),
		sandboxBackend: NewSandboxBackendMap(store, routingTTL),
	}
}
```

Update interface.go imports: add `"context"` (if absent) and `"github.com/e2bgateway/e2bgateway/internal/sessionstore"`.

- [ ] **Step 4: Wire the store in server.New**

In `internal/server/http.go`:

4a. Add `sessionStore sessionstore.Store` field to the `Server` struct (with import `"github.com/e2bgateway/e2bgateway/internal/sessionstore"`).

4b. In `New`, replace `s.registry = adapter.NewRegistry()` (line ~58) with:

```go
	// Shared data-plane session store (envd tokens + sandbox→backend routing).
	// type: memory (default) keeps single-replica behavior; type: redis
	// enables horizontal scaling.
	sessionStore, err := sessionstore.New(cfg.SessionStore)
	if err != nil {
		return nil, fmt.Errorf("initializing session store: %w", err)
	}
	s.sessionStore = sessionStore

	// Initialize adapter registry
	s.registry = adapter.NewRegistry(sessionStore, cfg.SessionStore.RoutingTTL)
```

(`err` is already declared later in `New` via `var err error` for authMgr — adjust so the file compiles: either move the `var err error` up or use `:=` consistently. Verify with `go build ./internal/server/`.)

- [ ] **Step 5: Update every call site of Set/Get/Delete**

Create paths — propagate (fail-closed). Kill paths — log with slog and continue. Exact edits:

`internal/adapter/agentsandbox/adapter.go:318` (CreateSandbox):

```go
		if err := a.registry.SandboxBackend().Set(e2bID, a.name); err != nil {
			return nil, fmt.Errorf("recording sandbox routing: %w", err)
		}
```

(Adjust the returned type to the function's actual error-return shape; the create function returns `(…, error)` — check the surrounding return statement and keep its non-error values zeroed.)

`internal/adapter/agentsandbox/adapter.go:425` (kill path):

```go
		if err := a.registry.SandboxBackend().Delete(sandboxID); err != nil {
			slog.Warn("failed to delete sandbox routing entry", "op", "routing_delete", "sandboxID", sandboxID, "backend", a.name, "error", err)
		}
```

(add `"log/slog"` import if the file lacks it — `warmpool.go` in the package already imports it, but each file needs its own import.)

`internal/adapter/opensandbox/adapter.go:393` and `:455` — wrap the `Set` calls the same way as agentsandbox:318 (propagate; match each enclosing function's return shape). `:518` — log-and-continue like agentsandbox:425 (`adapter.go` already imports `log/slog`).

`internal/adapter/e2bcloud/adapter.go:116,124` — propagate; `:169` — log-and-continue.

`internal/adapter/mock/adapter.go:121` — propagate; `:165` — log-and-continue.

`internal/server/envd_proxy.go:77-83` — replace with error-aware Get (the miss-fallback redesign is Task 7; keep the existing fallback for now):

```go
		if s.registry.SandboxBackend() != nil {
			name, found, err := s.registry.SandboxBackend().Get(sandboxID)
			if err != nil {
				slog.Error("session store error during sandbox routing lookup", "op", "routing_get", "sandboxID", sandboxID, "error", err)
				http.Error(w, `{"code":502,"message":"sandbox routing lookup failed"}`, http.StatusBadGateway)
				return
			}
			if found {
				backendName = name
				a, ok = s.registry.Get(name)
			}
		}
```

(add `"log/slog"` to envd_proxy.go imports if absent.)

`internal/server/http.go:76-79` — `CountByBackend()` signature unchanged; no edit needed.

- [ ] **Step 6: Fix all test constructions of NewRegistry**

Run: `grep -rn "NewRegistry()" --include="*_test.go" .`
Replace each with:

```go
NewRegistry(sessionstore.NewMemory(), time.Hour)
```

adding the sessionstore (and `time` if needed) imports to those test files.

- [ ] **Step 7: Run the full build + tests**

Run: `go build ./... && go test ./internal/adapter/... ./internal/server/... ./internal/sessionstore/ 2>&1 | grep -E "FAIL|ok" | head -15`
Expected: build clean, all listed packages `ok`.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -s -m "feat(adapter): back sandbox→backend routing map with the shared session store (#90)"
```

---

### Task 7: envd proxy — routing-miss 404 + store-error 502 on validation

**Files:**
- Modify: `internal/server/envd_proxy.go:85-111`
- Test: `internal/server/envd_proxy_test.go` (append; create with license header if absent — check `ls internal/server/*_test.go` first and follow the existing package name)

**Interfaces:**
- Consumes: error-aware `SandboxBackendMap.Get` (Task 6); `registry.List()` (verify it exists: `grep -n "func (r \*Registry) List" internal/adapter/interface.go` — the access-token handler already iterates it).
- Produces: proxy behavior — routing miss + exactly 1 adapter ⇒ use it; miss + ≠1 adapters ⇒ `404 {"code":404,"message":"sandbox not found"}`; `ValidateAccessToken` error ⇒ `502 {"code":502,"message":"token validation unavailable"}` (distinct from 401 invalid).

- [ ] **Step 1: Write the failing tests**

Append to the server package's envd proxy test file (reuse existing helpers in that package if present; otherwise the test uses `server.New` + `httptest` like `test/e2e/e2b_api_test.go` does — but these live in-package, constructing `Server` via `New(cfg)`):

```go
func TestEnvdProxy_RoutingMissMultiBackend404(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{HTTP: config.HTTPConfig{Address: "127.0.0.1:0"}},
		Backends: []config.BackendConfig{
			{Name: "mock-a", Type: "mock", Enabled: true},
			{Name: "mock-b", Type: "mock", Enabled: true},
		},
		Routing: config.RoutingConfig{DefaultBackend: "mock-a", Strategy: "static"},
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/some-envd-path", nil)
	req.Header.Set("E2b-Sandbox-Id", "unknown-sandbox")
	req.Header.Set("X-Access-Token", "whatever")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for routing miss with multiple backends, got %d", resp.StatusCode)
	}
}

func TestEnvdProxy_RoutingMissSingleBackendFallsThrough(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{HTTP: config.HTTPConfig{Address: "127.0.0.1:0"}},
		Backends: []config.BackendConfig{
			{Name: "mock", Type: "mock", Enabled: true},
		},
		Routing: config.RoutingConfig{DefaultBackend: "mock", Strategy: "static"},
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Unknown sandbox but a single registered adapter: the request must reach
	// token validation (mock has no token for it ⇒ 401), NOT 404.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/some-envd-path", nil)
	req.Header.Set("E2b-Sandbox-Id", "unknown-sandbox")
	req.Header.Set("X-Access-Token", "whatever")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 (fell through to sole adapter's validation), got %d", resp.StatusCode)
	}
}
```

(If the server package's existing tests construct `Server` differently, follow that pattern — check `internal/server/http_test.go` or similar first and mirror it. Imports: `net/http`, `net/http/httptest`, `testing`, `github.com/e2bgateway/e2bgateway/internal/config`.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/server/ -run TestEnvdProxy_RoutingMiss -v 2>&1 | tail -6`
Expected: `TestEnvdProxy_RoutingMissMultiBackend404` FAILS (current fallback picks a backend → 401, not 404).

- [ ] **Step 3: Implement**

In `internal/server/envd_proxy.go`, replace the fallback block (lines ~85-97, the `if !ok { name, err := s.routeMgr.SelectBackend(...) }` section) with:

```go
		// Routing miss. With the shared store this means the sandbox is
		// genuinely unknown (or its entry expired). A sole registered adapter
		// is still safe to try (single-backend deployments, sandboxes created
		// before an upgrade); with multiple adapters, picking one arbitrarily
		// produced misleading 401s — answer 404 instead.
		if !ok {
			adapters := s.registry.List()
			if len(adapters) != 1 {
				http.Error(w, `{"code":404,"message":"sandbox not found"}`, http.StatusNotFound)
				return
			}
			a = adapters[0]
			backendName = a.Name()
		}
```

Remove the now-unused `routing` import if `routeMgr`/`routing.RoutingRequest` is no longer referenced in this file (check: `grep -n "routing\." internal/server/envd_proxy.go`).

Then replace the validation block (lines ~100-111):

```go
		valid, err := a.ValidateAccessToken(r.Context(), sandboxID, token)
		if err != nil {
			slog.Error("access token validation failed", "op", "validate_token", "sandboxID", sandboxID, "backend", backendName, "error", err)
			http.Error(w, `{"code":502,"message":"token validation unavailable"}`, http.StatusBadGateway)
			return
		}
		if !valid {
			metrics.RecordTokenFailure(backendName, "invalid")
			http.Error(w, `{"code":401,"message":"invalid access token"}`, http.StatusUnauthorized)
			return
		}
```

(keep the existing `token == ""` missing-token check above it unchanged).

- [ ] **Step 4: Run tests**

Run: `go test ./internal/server/... 2>&1 | tail -3`
Expected: PASS (both new tests + all existing server tests — if an existing test asserted the old empty-RoutingRequest fallback behavior, update it to the new 404/401 semantics and note the change in the commit body).

- [ ] **Step 5: Commit**

```bash
git add internal/server/
git commit -s -m "fix(envd): 404 on routing miss with multiple backends, 502 on store errors (#90)"
```

---

### Task 8: agentsandbox tokens via shared store + `Aware` injection

**Files:**
- Modify: `internal/adapter/agentsandbox/adapter.go` — field `tokenCache` (~line 123) → `sessionStore`, constructor init (~line 248), `GetAccessToken`/`ValidateAccessToken` (~lines 1243-1282)
- Modify: `internal/server/http.go` — `initAdapters` (~line 417): inject store into `sessionstore.Aware` adapters
- Modify: `internal/sessionstore/store.go` — add the `Aware` interface
- Test: `internal/adapter/agentsandbox/token_store_test.go` (new)

**Interfaces:**
- Consumes: `sessionstore.GetToken/StoreTokenNX/OverwriteToken/ValidateToken` (Task 5), `sessionstore.NewMemory()` (Task 3).
- Produces:
```go
// sessionstore/store.go
type Aware interface{ SetSessionStore(store Store) }
// agentsandbox.Adapter
func (a *Adapter) SetSessionStore(s sessionstore.Store) // implements Aware
```
  Pattern for Task 9 (opensandbox): identical field/method/flow.

- [ ] **Step 1: Add the Aware interface**

Append to `internal/sessionstore/store.go`:

```go
// Aware is implemented by components that accept the shared store after
// construction. server.New builds adapters through their factories and then
// injects the process-wide store via this interface.
type Aware interface {
	SetSessionStore(store Store)
}
```

- [ ] **Step 2: Write the failing adapter test**

Create `internal/adapter/agentsandbox/token_store_test.go` (license header, `package agentsandbox`):

```go
package agentsandbox

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/sessionstore"
)

func newTokenTestAdapter(store sessionstore.Store) *Adapter {
	a := &Adapter{name: "agentsandbox-test"}
	a.SetSessionStore(store)
	return a
}

func TestGetAccessTokenSharedAcrossInstances(t *testing.T) {
	store := sessionstore.NewMemory()
	a1 := newTokenTestAdapter(store)
	a2 := newTokenTestAdapter(store) // "second replica"
	ctx := context.Background()

	tok1, err := a1.GetAccessToken(ctx, "sb-shared")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok1.Token, "envd_sb-shared_") {
		t.Errorf("unexpected token format: %q", tok1.Token)
	}

	tok2, err := a2.GetAccessToken(ctx, "sb-shared")
	if err != nil {
		t.Fatal(err)
	}
	if tok2.Token != tok1.Token {
		t.Fatalf("replica 2 minted a different token: %q vs %q", tok2.Token, tok1.Token)
	}

	// ExpiresAt must be the stored expiry, not re-stamped now+1h.
	if tok2.ExpiresAt.After(tok1.ExpiresAt) {
		t.Errorf("ExpiresAt was re-stamped: %v > %v", tok2.ExpiresAt, tok1.ExpiresAt)
	}

	valid, err := a2.ValidateAccessToken(ctx, "sb-shared", tok1.Token)
	if err != nil || !valid {
		t.Fatalf("cross-instance validation: (%v,%v)", valid, err)
	}
	valid, err = a2.ValidateAccessToken(ctx, "sb-shared", "wrong")
	if err != nil || valid {
		t.Fatalf("wrong token validated: (%v,%v)", valid, err)
	}
}

func TestGetAccessTokenConcurrentMintSingleWinner(t *testing.T) {
	store := sessionstore.NewMemory()
	const n = 8
	tokens := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a := newTokenTestAdapter(store)
			tok, err := a.GetAccessToken(context.Background(), "sb-race")
			if err != nil {
				t.Errorf("goroutine %d: %v", i, err)
				return
			}
			tokens[i] = tok.Token
		}(i)
	}
	wg.Wait()
	for i, tk := range tokens {
		if tk != tokens[0] {
			t.Fatalf("goroutine %d token %q != %q — SetNX arbitration broken", i, tk, tokens[0])
		}
	}
}

func TestValidateAccessTokenStoreError(t *testing.T) {
	a := newTokenTestAdapter(sessionstore.NewMemory())
	// No entry: false, nil (not an error).
	ok, err := a.ValidateAccessToken(context.Background(), "sb-none", "tok")
	if err != nil || ok {
		t.Fatalf("missing entry: (%v,%v)", ok, err)
	}
	_ = time.Hour // keep import if unused elsewhere
}
```

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/adapter/agentsandbox/ -run "TestGetAccessTokenShared|TestValidateAccessTokenStoreError" 2>&1 | tail -3`
Expected: compile FAIL — `a.SetSessionStore undefined`.

- [ ] **Step 4: Implement**

4a. In `internal/adapter/agentsandbox/adapter.go`:

- Replace the field (~line 123):

```go
	// Shared session store for envd access tokens (see internal/sessionstore).
	// Defaults to per-process memory; server.New injects the configured store.
	sessionStore sessionstore.Store
```

- In the constructor (~line 248) replace `tokenCache: cache.New(10000, 1*time.Hour),` with:

```go
		sessionStore: sessionstore.NewMemory(),
```

- Add the injection method (near GetAccessToken):

```go
const envdTokenTTL = 1 * time.Hour

// SetSessionStore injects the process-wide shared store (sessionstore.Aware).
func (a *Adapter) SetSessionStore(store sessionstore.Store) {
	a.sessionStore = store
}
```

- Replace `GetAccessToken` (lines ~1243-1268):

```go
// GetAccessToken returns a scoped access token for the sandbox. Tokens live
// in the shared session store, so any replica can validate them and they
// survive gateway restarts (with a redis-backed store). If two replicas mint
// concurrently, SetNX arbitration makes both return the same token.
func (a *Adapter) GetAccessToken(ctx context.Context, sandboxID string) (*adapter.AccessToken, error) {
	if tok, expiresAt, ok, err := sessionstore.GetToken(ctx, a.sessionStore, sandboxID); err != nil {
		return nil, fmt.Errorf("reading token entry: %w", err)
	} else if ok {
		return &adapter.AccessToken{Token: tok, ExpiresAt: expiresAt}, nil
	}

	// Generate new token: envd_{sandboxID}_{32-hex-random}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generating token: %w", err)
	}
	token := fmt.Sprintf("envd_%s_%s", sandboxID, hex.EncodeToString(b))
	expiresAt := time.Now().Add(envdTokenTTL)

	won, err := sessionstore.StoreTokenNX(ctx, a.sessionStore, sandboxID, token, expiresAt, envdTokenTTL)
	if err != nil {
		return nil, fmt.Errorf("storing token entry: %w", err)
	}
	if !won {
		// Another replica won the race — return its token.
		if tok, exp, ok, err := sessionstore.GetToken(ctx, a.sessionStore, sandboxID); err != nil {
			return nil, fmt.Errorf("reading winning token entry: %w", err)
		} else if ok {
			return &adapter.AccessToken{Token: tok, ExpiresAt: exp}, nil
		}
		// Winner's entry vanished (TTL edge): overwrite and use ours.
		if err := sessionstore.OverwriteToken(ctx, a.sessionStore, sandboxID, token, expiresAt, envdTokenTTL); err != nil {
			return nil, fmt.Errorf("storing token entry: %w", err)
		}
	}
	return &adapter.AccessToken{Token: token, ExpiresAt: expiresAt}, nil
}

// ValidateAccessToken checks the token against the shared session store.
// A missing entry is (false, nil); store failures propagate so the proxy can
// answer 502 instead of a misleading 401.
func (a *Adapter) ValidateAccessToken(ctx context.Context, sandboxID, token string) (bool, error) {
	return sessionstore.ValidateToken(ctx, a.sessionStore, sandboxID, token)
}
```

(The old `ValidateAccessToken` at ~1272-1282 is replaced by the above; the old signature ignored ctx with `_` — the new one uses it.)

- Remove the now-unused `"github.com/e2bgateway/e2bgateway/internal/cache"` import **if** `grep -n "cache\." internal/adapter/agentsandbox/adapter.go` shows no other uses (check first — remove only what is unused). Add the sessionstore import.

4b. In `internal/server/http.go` `initAdapters`, immediately after the successful adapter construction (after the `if err != nil { ... }` block, before `s.registry.Register(a)`), add:

```go
		// Inject the shared session store into adapters that issue envd tokens.
		if aware, isAware := a.(sessionstore.Aware); isAware {
			aware.SetSessionStore(s.sessionStore)
		}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/adapter/agentsandbox/ -race 2>&1 | tail -3 && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 6: Commit**

```bash
git add internal/adapter/agentsandbox/ internal/server/http.go internal/sessionstore/store.go
git commit -s -m "feat(agentsandbox): envd tokens via shared session store with SetNX arbitration (#90)"
```

---

### Task 9: opensandbox tokens via shared store

**Files:**
- Modify: `internal/adapter/opensandbox/adapter.go` — field `tokenCache` (~line 207 init), `GetAccessToken`/`getSignedAccessToken`/`generateRandomToken`/`ValidateAccessToken` (~lines 1059-1129)
- Test: `internal/adapter/opensandbox/token_store_test.go` (new)

**Interfaces:**
- Consumes: `sessionstore.Aware` + token helpers (Tasks 5, 8).
- Produces: `func (a *Adapter) SetSessionStore(s sessionstore.Store)` on the opensandbox adapter (injection wiring from Task 8 already covers it — `initAdapters` checks the interface for every adapter).

- [ ] **Step 1: Write the failing test**

Create `internal/adapter/opensandbox/token_store_test.go` (license header, `package opensandbox`):

```go
package opensandbox

import (
	"context"
	"strings"
	"testing"

	"github.com/e2bgateway/e2bgateway/internal/sessionstore"
)

func TestGetAccessTokenSharedStore(t *testing.T) {
	store := sessionstore.NewMemory()
	a1 := &Adapter{name: "osbx-a", useSignedEndpoint: false}
	a1.SetSessionStore(store)
	a2 := &Adapter{name: "osbx-b", useSignedEndpoint: false}
	a2.SetSessionStore(store)
	ctx := context.Background()

	tok1, err := a1.GetAccessToken(ctx, "sb-os")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok1.Token, "envd_sb-os_") {
		t.Errorf("unexpected token format: %q", tok1.Token)
	}
	tok2, err := a2.GetAccessToken(ctx, "sb-os")
	if err != nil {
		t.Fatal(err)
	}
	if tok2.Token != tok1.Token {
		t.Fatalf("second instance minted different token: %q vs %q", tok2.Token, tok1.Token)
	}
	valid, err := a2.ValidateAccessToken(ctx, "sb-os", tok1.Token)
	if err != nil || !valid {
		t.Fatalf("cross-instance validation: (%v,%v)", valid, err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/adapter/opensandbox/ -run TestGetAccessTokenSharedStore 2>&1 | tail -3`
Expected: compile FAIL — `a1.SetSessionStore undefined`.

- [ ] **Step 3: Implement**

Mirror Task 8's edits in `internal/adapter/opensandbox/adapter.go`:

- Replace `tokenCache` field with `sessionStore sessionstore.Store`; constructor line `tokenCache: cache.New(10000, 1*time.Hour),` → `sessionStore: sessionstore.NewMemory(),`. Remove the `internal/cache` import if no other uses remain (`grep -n "cache\." internal/adapter/opensandbox/*.go | grep -v _test`).
- Add:

```go
const envdTokenTTL = 1 * time.Hour

// SetSessionStore injects the process-wide shared store (sessionstore.Aware).
func (a *Adapter) SetSessionStore(store sessionstore.Store) {
	a.sessionStore = store
}
```

(If the package already declares `envdTokenTTL` elsewhere after this change, keep a single declaration.)

- Replace `GetAccessToken` (cache-check block → store check):

```go
func (a *Adapter) GetAccessToken(ctx context.Context, sandboxID string) (*adapter.AccessToken, error) {
	if tok, expiresAt, ok, err := sessionstore.GetToken(ctx, a.sessionStore, sandboxID); err != nil {
		return nil, fmt.Errorf("reading token entry: %w", err)
	} else if ok {
		return &adapter.AccessToken{Token: tok, ExpiresAt: expiresAt}, nil
	}

	if a.useSignedEndpoint {
		return a.getSignedAccessToken(ctx, sandboxID)
	}
	return a.generateRandomToken(ctx, sandboxID)
}
```

- Replace `getSignedAccessToken` (store the signed URL with its real expiry):

```go
func (a *Adapter) getSignedAccessToken(ctx context.Context, sandboxID string) (*adapter.AccessToken, error) {
	expiresAt := time.Now().Add(envdTokenTTL)
	ep, err := a.lifecycle.GetSignedEndpoint(ctx, sandboxID, 49983, expiresAt.Unix())
	if err != nil {
		return nil, fmt.Errorf("getting signed endpoint for sandbox %q: %w", sandboxID, err)
	}

	// The signed endpoint URL is the token.
	token := ep.Endpoint
	if !strings.HasPrefix(token, "http") {
		token = "http://" + token
	}

	won, err := sessionstore.StoreTokenNX(ctx, a.sessionStore, sandboxID, token, expiresAt, envdTokenTTL)
	if err != nil {
		return nil, fmt.Errorf("storing token entry: %w", err)
	}
	if !won {
		if tok, exp, ok, err := sessionstore.GetToken(ctx, a.sessionStore, sandboxID); err != nil {
			return nil, fmt.Errorf("reading winning token entry: %w", err)
		} else if ok {
			return &adapter.AccessToken{Token: tok, ExpiresAt: exp}, nil
		}
		if err := sessionstore.OverwriteToken(ctx, a.sessionStore, sandboxID, token, expiresAt, envdTokenTTL); err != nil {
			return nil, fmt.Errorf("storing token entry: %w", err)
		}
	}
	return &adapter.AccessToken{Token: token, ExpiresAt: expiresAt}, nil
}
```

- Replace `generateRandomToken` (gains ctx; same mint + SetNX flow as agentsandbox Task 8 Step 4a):

```go
func (a *Adapter) generateRandomToken(ctx context.Context, sandboxID string) (*adapter.AccessToken, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generating token: %w", err)
	}
	token := fmt.Sprintf("envd_%s_%s", sandboxID, hex.EncodeToString(b))
	expiresAt := time.Now().Add(envdTokenTTL)

	won, err := sessionstore.StoreTokenNX(ctx, a.sessionStore, sandboxID, token, expiresAt, envdTokenTTL)
	if err != nil {
		return nil, fmt.Errorf("storing token entry: %w", err)
	}
	if !won {
		if tok, exp, ok, err := sessionstore.GetToken(ctx, a.sessionStore, sandboxID); err != nil {
			return nil, fmt.Errorf("reading winning token entry: %w", err)
		} else if ok {
			return &adapter.AccessToken{Token: tok, ExpiresAt: exp}, nil
		}
		if err := sessionstore.OverwriteToken(ctx, a.sessionStore, sandboxID, token, expiresAt, envdTokenTTL); err != nil {
			return nil, fmt.Errorf("storing token entry: %w", err)
		}
	}
	return &adapter.AccessToken{Token: token, ExpiresAt: expiresAt}, nil
}
```

- Replace `ValidateAccessToken`:

```go
func (a *Adapter) ValidateAccessToken(ctx context.Context, sandboxID, token string) (bool, error) {
	return sessionstore.ValidateToken(ctx, a.sessionStore, sandboxID, token)
}
```

- Fix any internal callers of `generateRandomToken` for the new ctx parameter (`grep -n "generateRandomToken" internal/adapter/opensandbox/`).

- [ ] **Step 4: Run tests**

Run: `go test ./internal/adapter/opensandbox/ -race 2>&1 | tail -3 && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/opensandbox/
git commit -s -m "feat(opensandbox): envd tokens via shared session store (#90)"
```

---

### Task 10: Deploy manifests, example configs, docs, CHANGELOG

**Files:**
- Modify: `deploy/helm/e2bgateway/values.yaml` (config block), `deploy/helm/e2bgateway/templates/NOTES.txt` (~line 88 warning)
- Modify: `deploy/kustomize/base/configmap.yaml`, `deploy/kustomize/overlays/production/deployment-patch.yaml`, `deploy/kustomize/overlays/staging/deployment-patch.yaml`
- Modify: `configs/e2bgateway-example.yaml`, `configs/e2bgateway-default.yaml` (commented example), `AGENTS.md`, `CHANGELOG.md`

**Interfaces:**
- Consumes: finalized config surface (Task 2).
- Produces: operator-facing docs matching implemented behavior.

- [ ] **Step 1: helm values.yaml — add sessionStore under `config:`** (next to `auth:`/`rateLimit:`, top level of the gateway config, NOT per-backend):

```yaml
  # Shared data-plane session store (envd access tokens + sandbox→backend
  # routing). REQUIRED type: redis before running more than one replica —
  # with type: memory each replica only knows its own sandboxes (401s on the
  # envd data plane). Redis must be access-controlled; password + non-loopback
  # addr requires TLS (tls: true or rediss:// addr).
  sessionStore:
    type: memory              # memory | redis
    # addr: "redis:6379"      # required when type: redis
    # password: ""
    # tls: false
    # db: 0
    # keyPrefix: "e2bgw:session:"
    # routingTTL: 24h         # sandbox→backend entry TTL, refreshed on read
```

- [ ] **Step 2: NOTES.txt — replace the single-replica warning** (~line 88; keep it prominent, update the text):

Find the existing warning about per-process in-memory state / do-not-scale and replace with:

```
WARNING: replicas={{ .Values.replicaCount }} with sessionStore.type={{ .Values.config.sessionStore.type | default "memory" }}.
Running more than one replica REQUIRES sessionStore.type=redis (shared envd
access tokens + sandbox routing); with type=memory, data-plane requests that
land on a different replica than the creator get 401. See values.yaml.
```

(render only when replicaCount > 1 and sessionStore type is memory — wrap in `{{- if and (gt (int .Values.replicaCount) 1) (ne (.Values.config.sessionStore.type | default "memory") "redis") }}` … `{{- end }}`; if `.Values.replicaCount` doesn't exist in this chart use the actual replicas value path — check `grep -n "replicas" deploy/helm/e2bgateway/values.yaml deploy/helm/e2bgateway/templates/deployment.yaml` and adapt.)

- [ ] **Step 3: kustomize base configmap — add commented sessionStore block** (top level of the embedded gateway config, e.g. after the `auth:` section):

```yaml
    # Shared data-plane session store. REQUIRED type: redis for replicas > 1
    # (envd tokens + sandbox routing are otherwise per-process → 401s).
    sessionStore:
      type: memory
      # addr: "redis:6379"
      # password: ""
      # tls: false              # REQUIRED true (or rediss://) when password is set and addr is not loopback
      # db: 0
      # keyPrefix: "e2bgw:session:"
      # routingTTL: 24h
```

- [ ] **Step 4: overlays — update the pinned-replicas comments** in `deploy/kustomize/overlays/production/deployment-patch.yaml` and `staging/…`: keep `replicas: 1` but replace the "per-process in-memory state… 401" wording with: "safe to scale once `sessionStore.type: redis` is configured (see base configmap); until then keep 1 replica."

- [ ] **Step 5: configs examples** — in `configs/e2bgateway-example.yaml` add (top level, before `auth:`):

```yaml
# Shared data-plane session store (envd access tokens + sandbox→backend
# routing map). type: memory (default) keeps per-process state — fine for a
# single replica. REQUIRED type: redis for replicas > 1 / HPA. Works on and
# off Kubernetes: any reachable Redis; TLS enforced when a password is set
# and addr is not loopback (tls: true or rediss:// addr).
sessionStore:
  type: redis
  addr: "redis:6379"
  password: ""
  tls: false
  db: 0
  keyPrefix: "e2bgw:session:"
  routingTTL: 24h
```

In `configs/e2bgateway-default.yaml` add the memory-default variant as a comment (do NOT change runtime behavior of the default config):

```yaml
# Shared data-plane session store (envd tokens + sandbox→backend routing).
# Default (memory) is per-process — single replica only. Set type: redis
# (addr/password/tls/db/keyPrefix/routingTTL) before scaling out.
# sessionStore:
#   type: memory
```

- [ ] **Step 6: AGENTS.md** — add a `sessionStore` row/section next to the existing config documentation (find the auth/cache config sections: `grep -n "rateLimit\|templateStore" AGENTS.md | head`), summarizing: purpose (horizontal scaling), memory|redis, key layout (`{prefix}token:{id}` TTL 1h, `{prefix}routing:{id}` TTL routingTTL refresh-on-read), TLS rule, and "replicas > 1 requires redis".

- [ ] **Step 7: CHANGELOG.md** — under `## Unreleased` add:

```markdown
* [FEATURE] Horizontal scaling: new top-level `sessionStore` config (`memory` | `redis`) persists envd access tokens and the sandbox→backend routing map in a shared store, so any replica can serve data-plane requests and tokens survive gateway restarts. Includes SetNX token arbitration, real (not re-stamped) `expiresAt`, 404 on envd-proxy routing miss with multiple backends (was: arbitrary backend → 401), and 502 on session-store outages. `replicas > 1` requires `sessionStore.type: redis`. #90
```

- [ ] **Step 8: Verify rendering**

Run: `helm lint deploy/helm/e2bgateway && (kubectl kustomize deploy/kustomize/base >/dev/null && echo KUSTOMIZE_OK) && helm template test deploy/helm/e2bgateway >/dev/null && echo HELM_OK`
Expected: lint passing, `KUSTOMIZE_OK`, `HELM_OK`.

- [ ] **Step 9: Commit**

```bash
git add deploy/ configs/ AGENTS.md CHANGELOG.md
git commit -s -m "docs(deploy): document sessionStore for multi-replica deployments (#90)"
```

---

### Task 11: Multi-replica kind E2E

**Files:**
- Create: `test/e2e/multi_replica_test.go`
- Modify: `.github/workflows/e2e.yml` — new job `e2e-multi-replica` (clone of `e2e-agent-sandbox` with the modifications below)

**Interfaces:**
- Consumes: gateway behavior from Tasks 6-9; existing workflow conventions (kind cluster, image build, controller deploy, gateway ConfigMap at workflow lines ~272-330, `Run E2E tests` step at ~391).
- Produces: CI proof of the issue's acceptance criteria — create on replica A, data-plane on replica B without 401, restart recovery.

- [ ] **Step 1: Write the Go E2E test**

Create `test/e2e/multi_replica_test.go` (license header, `package e2e`; follows the env-gated pattern of the existing suite — check `grep -n "os.Getenv\|Skip" test/e2e/*.go | head` and mirror the convention):

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

	"github.com/e2bgateway/e2bgateway/internal/api/dto"
)

// TestMultiReplicaDataPlane runs against TWO port-forwards pinned to two
// different gateway replicas (GATEWAY_URL → pod A, GATEWAY_URL_B → pod B),
// both backed by a shared redis sessionStore. It proves issue #90's core
// acceptance: a sandbox created via replica A serves data-plane requests on
// replica B with no 401.
func TestMultiReplicaDataPlane(t *testing.T) {
	urlA := os.Getenv("GATEWAY_URL")
	urlB := os.Getenv("GATEWAY_URL_B")
	if urlA == "" || urlB == "" {
		t.Skip("GATEWAY_URL / GATEWAY_URL_B not set — multi-replica E2E only")
	}
	apiKey := os.Getenv("E2B_API_KEY")
	client := &http.Client{Timeout: 60 * time.Second}

	// 1. Create a sandbox pinned to replica A.
	createBody, _ := json.Marshal(dto.SandboxCreateRequest{TemplateID: "base"})
	req, _ := http.NewRequest(http.MethodPost, urlA+"/sandboxes", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("create on replica A: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("create on replica A: status %d body %s", resp.StatusCode, body)
	}
	var created dto.SandboxCreateResponse
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decoding create response: %v (%s)", err, body)
	}
	if created.SandboxID == "" || created.EnvdAccessToken == "" {
		t.Fatalf("create response missing sandboxID/envdAccessToken: %s", body)
	}
	t.Cleanup(func() {
		delReq, _ := http.NewRequest(http.MethodDelete, urlA+"/sandboxes/"+created.SandboxID, nil)
		delReq.Header.Set("X-API-Key", apiKey)
		delResp, err := client.Do(delReq)
		if err == nil {
			_ = delResp.Body.Close()
		}
	})

	// 2. Data-plane request pinned to replica B with the token from A.
	// Any unmatched path routes into the envd proxy.
	dpReq, _ := http.NewRequest(http.MethodGet, urlB+"/multi-replica-probe", nil)
	dpReq.Header.Set("E2b-Sandbox-Id", created.SandboxID)
	dpReq.Header.Set("X-Access-Token", created.EnvdAccessToken)
	dpResp, err := client.Do(dpReq)
	if err != nil {
		t.Fatalf("data-plane request on replica B: %v", err)
	}
	defer dpResp.Body.Close()
	dpBody, _ := io.ReadAll(dpResp.Body)
	// A pass must PROVE validation+routing worked: 401 (token rejected),
	// 502 (store outage) and the gateway's own "sandbox not found" 404 body
	// (routing miss) are all failures; any other response reached envd.
	switch dpResp.StatusCode {
	case http.StatusUnauthorized:
		t.Fatalf("replica B rejected replica A's token with 401 (issue #90 not fixed): %s", dpBody)
	case http.StatusBadGateway:
		t.Fatalf("replica B could not reach the session store (502) — cross-replica validation unproven: %s", dpBody)
	}
	if strings.Contains(string(dpBody), "sandbox not found") {
		t.Fatalf("replica B has no routing entry for replica A's sandbox (gateway 404): %s", dpBody)
	}

	// 3. A wrong token must still be rejected on replica B (validation is real).
	badReq, _ := http.NewRequest(http.MethodGet, urlB+"/multi-replica-probe", nil)
	badReq.Header.Set("E2b-Sandbox-Id", created.SandboxID)
	badReq.Header.Set("X-Access-Token", "envd_wrong_token")
	badResp, err := client.Do(badReq)
	if err != nil {
		t.Fatalf("bad-token request: %v", err)
	}
	defer badResp.Body.Close()
	if badResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong token on replica B, got %d", badResp.StatusCode)
	}
}
```

(If `dto.SandboxCreateRequest`/`SandboxCreateResponse` field names differ, adjust to the real DTO — check `grep -n "SandboxCreateRequest struct" -A 10 internal/api/dto/e2b.go`. If the existing e2e suite has a `doJSON`-style helper usable with an absolute URL, prefer consistency with it.)

Run locally (no env set) to confirm it skips: `go test ./test/e2e/ -run TestMultiReplicaDataPlane -v` → `SKIP`.

- [ ] **Step 2: Add the workflow job**

In `.github/workflows/e2e.yml`, duplicate the entire `e2e-agent-sandbox` job as `e2e-multi-replica` (job `name: E2E (multi-replica)`, its own kind cluster name `e2bgateway-e2e-mr`), then apply these modifications to the copy:

2a. After the agent-sandbox controller deploy steps, add a Redis deploy step:

```yaml
      - name: Deploy Redis
        run: |
          kubectl create namespace e2bgateway-system --dry-run=client -o yaml | kubectl apply -f -
          cat <<EOF | kubectl apply -f -
          apiVersion: apps/v1
          kind: Deployment
          metadata:
            name: redis
            namespace: e2bgateway-system
          spec:
            replicas: 1
            selector:
              matchLabels: {app: redis}
            template:
              metadata:
                labels: {app: redis}
              spec:
                containers:
                  - name: redis
                    image: redis:7-alpine
                    ports:
                      - containerPort: 6379
                    readinessProbe:
                      tcpSocket: {port: 6379}
                      initialDelaySeconds: 2
                      periodSeconds: 3
          ---
          apiVersion: v1
          kind: Service
          metadata:
            name: redis
            namespace: e2bgateway-system
          spec:
            selector: {app: redis}
            ports:
              - port: 6379
                targetPort: 6379
          EOF
          kubectl rollout status deployment/redis -n e2bgateway-system --timeout=120s
```

2b. In the `Deploy e2bgateway` step's inline ConfigMap, add to the gateway config YAML (top level):

```yaml
          sessionStore:
            type: redis
            addr: "redis.e2bgateway-system.svc:6379"
            keyPrefix: "e2bgw:session:"
            routingTTL: 24h
```

2c. In the gateway Deployment (same step or `test/kind-e2e/manifests/gateway/deployment.yaml` if the job uses it): set `replicas: 2`. If the manifest is shared with the single-replica job, do NOT edit the shared file — patch inline in this job only (`kubectl patch deployment e2bgateway -n e2bgateway-system --replicas=2` after apply, or a job-local copy of the manifest).

2d. Replace the `Run E2E tests` step with pinned per-pod port-forwards:

```yaml
      - name: Run multi-replica E2E
        run: |
          set -euo pipefail
          kubectl rollout status deployment/e2bgateway -n e2bgateway-system --timeout=180s
          PODS=($(kubectl get pods -n e2bgateway-system -o name | grep e2bgateway | grep -v redis | head -2))
          if [ "${#PODS[@]}" -ne 2 ]; then echo "expected 2 gateway pods, got ${#PODS[@]}"; exit 1; fi
          kubectl port-forward -n e2bgateway-system "${PODS[0]}" 8080:8080 &
          kubectl port-forward -n e2bgateway-system "${PODS[1]}" 8081:8080 &
          sleep 5
          export GATEWAY_URL="http://localhost:8080"
          export GATEWAY_URL_B="http://localhost:8081"
          export E2B_API_KEY="e2b_0000000000000000000000000000000000000000"
          go test ./test/e2e/ -run TestMultiReplicaDataPlane -v -count=1

          # Restart recovery: old token must survive a full gateway restart.
          TOKEN=$(curl -sf -X POST "$GATEWAY_URL/sandboxes" \
            -H "Content-Type: application/json" -H "X-API-Key: $E2B_API_KEY" \
            -d '{"templateID":"base"}' | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d["sandboxID"], d["envdAccessToken"])')
          SBX_ID="${TOKEN%% *}"; TOK="${TOKEN##* }"
          kubectl rollout restart deployment/e2bgateway -n e2bgateway-system
          kubectl rollout status deployment/e2bgateway -n e2bgateway-system --timeout=180s
          kill %1 %2 2>/dev/null || true
          PODS=($(kubectl get pods -n e2bgateway-system -o name | grep e2bgateway | grep -v redis | head -2))
          kubectl port-forward -n e2bgateway-system "${PODS[1]}" 8081:8080 &
          sleep 5
          BODY_FILE=$(mktemp)
          CODE=$(curl -s -o "$BODY_FILE" -w "%{http_code}" "http://localhost:8081/multi-replica-probe" \
            -H "E2b-Sandbox-Id: $SBX_ID" -H "X-Access-Token: $TOK")
          BODY=$(cat "$BODY_FILE"); rm -f "$BODY_FILE"
          # 401 = token rejected; 502 = store outage; gateway 404 body =
          # routing lost. Anything else proves the token survived restart.
          if [ "$CODE" = "401" ]; then echo "restart recovery FAILED: old token rejected (401): $BODY"; exit 1; fi
          if [ "$CODE" = "502" ]; then echo "restart recovery FAILED: session-store outage (502): $BODY"; exit 1; fi
          if echo "$BODY" | grep -q "sandbox not found"; then echo "restart recovery FAILED: routing entry lost: $BODY"; exit 1; fi
          echo "restart recovery OK (status $CODE — token validated, request routed)"
          curl -s -X DELETE "$GATEWAY_URL/sandboxes/$SBX_ID" -H "X-API-Key: $E2B_API_KEY" >/dev/null || true
```

(Adapt pod label/name greps to the actual gateway deployment name — verify with `grep -n "name: e2bgateway\|app" test/kind-e2e/manifests/gateway/deployment.yaml | head`. If the create response JSON field is `sandboxID`, keep as written; confirm against `internal/api/dto/e2b.go:40-55`.)

- [ ] **Step 3: Validate workflow YAML**

Run: `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/e2e.yml')); print('YAML OK')"`
Expected: `YAML OK`. (Full validation happens when CI runs the job on push.)

- [ ] **Step 4: Commit**

```bash
git add test/e2e/multi_replica_test.go .github/workflows/e2e.yml
git commit -s -m "test(e2e): 2-replica kind E2E for shared session store + restart recovery (#90)"
```

---

### Task 12: Full verification gate

**Files:** none (verification; fix-forward commits if needed)

- [ ] **Step 1: Full gate**

Run: `make pre-commit`
Expected: exit 0 (fmt + vet + golangci-lint + full test suite). Fix any lint findings (watch: depguard stdlib restrictions, unused imports after the `internal/cache` removals) and re-run.

- [ ] **Step 2: Memory-mode smoke (default behavior unchanged)**

```bash
cat > /tmp/e2bgw-mem.yaml <<'EOF'
server:
  http:
    address: "127.0.0.1:18081"
backends:
  - name: mock
    type: mock
    enabled: true
EOF
go run ./cmd/e2bgateway serve --config /tmp/e2bgw-mem.yaml &>/tmp/e2bgw-mem.log &
GW=$!; sleep 4
CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST http://127.0.0.1:18081/sandboxes -H "Content-Type: application/json" -d '{"templateID":"base"}')
echo "create status=$CODE (expect 200/201)"
kill $GW; rm -f /tmp/e2bgw-mem.yaml /tmp/e2bgw-mem.log
```

(If the default config enables auth, add the API-key header per `configs/e2bgateway-default.yaml`; the assertion is create succeeds and the gateway logs show no session-store errors.)

- [ ] **Step 3: Fail-fast smoke (redis configured but unreachable)**

```bash
cat > /tmp/e2bgw-redis.yaml <<'EOF'
server:
  http:
    address: "127.0.0.1:18082"
backends:
  - name: mock
    type: mock
    enabled: true
sessionStore:
  type: redis
  addr: "127.0.0.1:1"
EOF
go run ./cmd/e2bgateway serve --config /tmp/e2bgw-redis.yaml 2>&1 | tail -2; echo "exit above must be non-zero"
rm -f /tmp/e2bgw-redis.yaml
```

Expected: startup error mentioning `session store redis unreachable`, non-zero exit.

- [ ] **Step 4: Branch state**

Run: `git status --short && git log --oneline main..HEAD`
Expected: clean tree; 12 commits (Tasks 1-11 + any fix-forwards).

- [ ] **Step 5 (only if fixes were needed): Commit**

```bash
git add -A
git commit -s -m "fix(sessionstore): address pre-commit findings (#90)"
```
