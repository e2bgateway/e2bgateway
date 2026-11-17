# Shared Session Store for Horizontal Scaling

**Issue**: #90 — Support horizontal scaling: persist envd access-token cache and sandbox→backend routing map in a shared store (Redis)
**Date**: 2026-11-16
**Priority**: P1
**Approach**: 1 — New `internal/sessionstore` generic KV package (memory + redis), injected into registry and token-issuing adapters

---

## 1. Problem Statement

The gateway cannot run with more than one replica. Two pieces of data-plane
session state are per-process in-memory only:

1. **envd access-token cache** — `GetAccessToken` mints a random
   `envd_{sandboxID}_{32hex}` token and stores it in a process-local LRU
   (`internal/cache`, cap 10000 / TTL 1h) in both the agentsandbox
   (`internal/adapter/agentsandbox/adapter.go:1243-1268`) and opensandbox
   (`internal/adapter/opensandbox/adapter.go:1059-1074`) adapters. The envd
   reverse proxy validates the token against **the same process's** cache
   (`internal/server/envd_proxy.go:100-111` → `ValidateAccessToken`).
2. **sandbox→backend routing map** — `SandboxBackendMap`
   (`internal/adapter/interface.go:422-470`) is an in-process
   `map[string]string`. On a miss the proxy falls back to an empty
   `RoutingRequest` (`envd_proxy.go:85-97`), which can select the wrong
   backend in multi-backend setups.

With N replicas behind a Service, sandbox creation may land on replica A while
data-plane requests are load-balanced to replica B. B has no token → stable
401s; B also does not know which backend owns the sandbox. A gateway restart
has the same effect (tokens are regenerated on miss, so the SDK's old token
can never validate again). Deploy manifests currently pin `replicas: 1` with
warning comments as an interim mitigation
(`deploy/kustomize/overlays/production/deployment-patch.yaml`,
`deploy/helm/e2bgateway/templates/NOTES.txt:88`).

## 2. Design Goals

- Any replica can validate any envd access token and route any data-plane
  request: gateway runs correctly with `replicas > 1` / HPA.
- Gateway restarts no longer invalidate outstanding SDK tokens (when the
  shared store is Redis-backed).
- Follow the existing warm-pool `stateStore` pattern: `type: memory | redis`
  with addr/password/tls/db/keyPrefix fields — but with a STRICTER TLS rule
  (review-hardened): because the store carries envd bearer tokens, ANY
  non-loopback addr requires TLS regardless of password (`rediss://` implies
  TLS). `allowPlaintext: true` is an explicit, warn-logged opt-out for
  trusted networks (e.g. in-cluster redis behind NetworkPolicy); it does not
  waive the password-requires-TLS rule.
- `type: memory` (default) preserves today's single-process behavior exactly —
  zero-config deployments are unaffected.
- Fix the wrong-backend fallback on routing miss.
- E2E proof: 2-replica kind deployment — create via replica A, data-plane via
  replica B, no 401; restart recovery.

**Non-goals (this iteration):**

- e2bcloud adapter changes (upstream validates tokens — already replica-safe).
- Mock adapter changes (single-process testing aid; keeps in-memory behavior).
- Shared rate-limit or template stores (the unwired `internal/cache/redis.go`
  and `RateLimitConfig.Backend=redis` paths stay as-is).
- Hot reconfiguration of the store; it is built once at startup.
- Making token TTL configurable (stays at the existing 1h semantics).

## 3. Architecture

### 3.1 New package `internal/sessionstore`

```go
// Store is a TTL-aware string KV store shared across gateway replicas.
type Store interface {
    Get(ctx context.Context, key string) (value string, ok bool, err error)
    Set(ctx context.Context, key, value string, ttl time.Duration) error
    // SetNX writes only if key is absent; reports whether it wrote.
    // Used to arbitrate concurrent token generation across replicas.
    SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error)
    // Touch extends the expiry of an existing key WITHOUT rewriting its
    // value; a missing or expired key is a no-op (never resurrected).
    Touch(ctx context.Context, key string, ttl time.Duration) error
    Delete(ctx context.Context, key string) error
    // Scan visits all live keys with the given prefix (used for metrics).
    Scan(ctx context.Context, prefix string, visit func(key, value string)) error
}

// Aware is implemented by adapters that need the shared store injected
// after construction (agentsandbox, opensandbox).
type Aware interface {
    SetSessionStore(store Store)
}

func New(cfg config.SessionStoreConfig) (Store, error) // memory | redis dispatch, fail fast
```

- **memory implementation**: own `sync.RWMutex` + `map[string]entry{value,
  expiresAt}` with lazy expiry on Get/Scan; atomic SetNX under the lock. Does
  not reuse `internal/cache` (no ctx, no SetNX, no Scan there).
- **redis implementation**: `github.com/redis/go-redis/v9` (existing dep);
  GET / SET EX / SET NX EX / PEXPIRE (Touch — millisecond precision, no
  value rewrite) / DEL / SCAN. `ttl <= 0` means persist (SET without EX).
  `New` pings Redis at startup — unreachable ⇒ startup failure.
- **Shared TLS helpers**: move `normalizeRedisAddr` and `redisTLSConfig` from
  `internal/adapter/opensandbox/redis_tls.go` into a new shared package
  `internal/redisx`; opensandbox (warmpool, templatestore) and sessionstore
  both consume it. The helpers keep the warm-pool CWE-319 rule (password set
  + non-loopback addr requires TLS; `rediss://` implies TLS; no insecure
  skip); the session store layers the STRICTER policy on top (any
  non-loopback addr requires TLS, `allowPlaintext` to opt out explicitly).

### 3.2 Config (`internal/config`)

New top-level block, fields mirroring `PoolStateStoreConfig`:

```yaml
sessionStore:
  type: memory              # memory | redis (default memory)
  addr: "redis:6379"        # required when type: redis; rediss:// implies TLS
  password: ""
  tls: false                # REQUIRED true for ANY non-loopback addr (bearer tokens)
  allowPlaintext: false     # explicit trusted-network opt-out; warns at startup
  db: 0
  keyPrefix: "e2bgw:session:"
  routingTTL: 24h           # sandbox→backend mapping TTL, refreshed on read
```

```go
type SessionStoreConfig struct {
    Type           string        `mapstructure:"type"`
    Addr           string        `mapstructure:"addr"`
    Password       string        `mapstructure:"password"`
    DB             int           `mapstructure:"db"`
    KeyPrefix      string        `mapstructure:"keyPrefix"`
    EnableTLS      bool          `mapstructure:"tls"`
    RoutingTTL     time.Duration `mapstructure:"routingTTL"`
    AllowPlaintext bool          `mapstructure:"allowPlaintext"`
}
```

Validation (in `Config.Validate()`, mirroring `validatePoolStateStoreConfig`):
unknown type ⇒ error; `type: redis` without addr ⇒ error; TLS rule enforced
via `redisx`; `routingTTL <= 0` ⇒ default 24h; negative db ⇒ error. Token TTL
is not configurable (1h, existing semantics).

### 3.3 Key layout

| Data | Key | Value | TTL |
|---|---|---|---|
| envd access token | `{keyPrefix}token:{sandboxID}` | JSON `{"token":"...","expiresAt":"<RFC3339>"}` | 1h (signed-endpoint mode: the signed URL's own expiry) |
| sandbox→backend | `{keyPrefix}routing:{sandboxID}` | backend name | `routingTTL` (default 24h), refreshed on every Get hit |

Storing `expiresAt` inside the value fixes a latent bug: today
`GetAccessToken` re-stamps `ExpiresAt: now+1h` on every cache hit instead of
reporting the real expiry.

### 3.4 Token flow (agentsandbox + opensandbox)

`server.New` builds the store once from `cfg.SessionStore` and:

1. passes it to `adapter.NewRegistry(store)` (signature change) so
   `SandboxBackendMap` becomes store-backed;
2. after constructing each adapter in `initAdapters`
   (`internal/server/http.go:417`), injects via the `Aware` interface —
   factory signatures unchanged; e2bcloud/mock simply don't implement it.

Adapter changes (agentsandbox and opensandbox; the in-process
`tokenCache *cache.Cache` field is **removed** — one path, no dual-write):

- `GetAccessToken(ctx, sandboxID)`:
  1. `store.Get(tokenKey)` → hit: unmarshal, return stored token + real
     `ExpiresAt`.
  2. Miss: mint token (random mode: `envd_{sandboxID}_{32hex}` via
     crypto/rand; opensandbox signed mode: `GetSignedEndpoint` URL as today) →
     `SetNX`. If SetNX returns false (another replica won the race), `Get`
     again and return the winner's token. **One sandbox ⇒ exactly one live
     token cluster-wide.**
- `ValidateAccessToken(ctx, sandboxID, token)`: `store.Get` → compare;
  miss ⇒ `(false, nil)`; store error ⇒ `(false, err)`.
- Restart recovery: with Redis the token survives gateway restarts, so the
  SDK's outstanding token keeps validating (issue's core requirement). With
  memory, behavior matches today (regenerated on restart).
- Kill paths keep `SandboxBackend().Delete` (routing); tokens expire by TTL
  as today (no new delete call — parity with current behavior).

### 3.5 Routing map flow (registry)

`SandboxBackendMap` keeps its name but its methods gain error returns and
delegate to the `Store` (fail-closed: a sandbox whose routing entry cannot be
recorded must not be reported as created):

```go
Set(sandboxID, backendName string) error
Get(sandboxID string) (backendName string, ok bool, err error)
Delete(sandboxID string) error
Size() int                       // best effort; 0 on store error (metrics only)
CountByBackend() map[string]int  // best effort (metrics only)
```

- `Set` ⇒ `store.Set(routingKey, backend, routingTTL)`; error propagates —
  create paths in all four adapters (the `SandboxBackend().Set` calls in
  agentsandbox/opensandbox/e2bcloud/mock) fail the creation instead of
  leaving an unroutable sandbox behind.
- `Get` ⇒ `store.Get`; on hit, refresh TTL via `store.Touch` (expiry-only —
  never rewrites the value, so a read cannot clobber a concurrent `Set` or
  resurrect a concurrent `Delete`). Callers
  (envd proxy, `http.go`) handle the error: proxy returns 502.
- `Delete` ⇒ error logged at kill paths (delete failure must not block kill;
  the entry expires via TTL).
- No ctx parameters (keeps the change bounded); the implementation uses
  `context.Background()`, with redis client timeouts bounding calls.
- `Size`/`CountByBackend` (consumed by the metrics `SandboxCounts` source,
  `internal/server/http.go:76-79`): memory = direct counts; redis = `Scan`
  aggregation — acceptable at metrics-scrape frequency; store errors yield
  empty results (observability is best effort).

### 3.6 envd proxy routing-miss fix

`envd_proxy.go:85-97`: on a `SandboxBackend().Get` miss (now authoritative —
shared across replicas), stop falling back to an empty `RoutingRequest`:

- exactly one adapter registered ⇒ use it (preserves single-backend
  deployments' convenience, e.g. pre-existing sandboxes created before an
  upgrade);
- more than one ⇒ `404` JSON error `{"code":"sandbox_not_found"}` instead of
  silently picking a backend that cannot validate the token.

### 3.7 Data flow

```
                      ┌────────────────────────────┐
                      │  sessionStore (Redis)      │
                      │  token:{id}   (TTL 1h)     │
                      │  routing:{id} (TTL 24h ↻)  │
                      └───────▲───────────▲────────┘
              SetNX/Get       │           │  Get/Set/Delete/Scan
   ┌──────────────────┐  ┌────┴─────┐ ┌───┴───────────────┐
   │ replica A        │  │ replica B│ │ registry          │
   │ POST /sandboxes  │  │ envd     │ │ SandboxBackendMap │
   │ GetAccessToken ──┘  │ proxy    │ └───────────────────┘
   └──────────────────┘  │ Validate─┘  (shared by both replicas)
                         └──────────
```

## 4. API Surface

No HTTP API additions. Behavior changes:

- envd proxy routing miss with multiple backends: previously an arbitrary
  backend was tried (typically ending in 401), now `404 sandbox_not_found`.
- `GetAccessToken` responses report the token's real `expiresAt` instead of
  now+1h on every call.
- Store outage during validation: `502` (see §6), not `401`.

## 5. Configuration

New `sessionStore` block (§3.2) at the top level of the gateway config.
Defaults keep current behavior (`type: memory`). Example configs
(`configs/e2bgateway-example.yaml`, `configs/e2bgateway-default.yaml` as a
comment), `AGENTS.md`, helm `values.yaml` / README, and kustomize configmap
gain documented examples. Deploy warnings change from "do not scale" to
"configure `sessionStore.type: redis` before running more than one replica";
default `replicas: 1` stays (Redis is operator-provided, not bundled).

Viper note: `sessionStore` is a struct with `mapstructure` tags, not a map —
no lowercasing hazard. Adapter factories are not involved (injection is via
the `Aware` interface, not backend config maps).

## 6. Security Considerations

- **Tokens in Redis**: access tokens are bearer credentials at rest in the
  shared store. Mitigations: the stricter TLS-enforcement rule (ANY
  non-loopback ⇒ TLS required, `rediss://` implicit; `allowPlaintext: true`
  is an explicit warn-logged trusted-network opt-out that never waives the
  password-requires-TLS rule), 1h TTL bounding exposure, keys namespaced
  under `keyPrefix`. Documented in example configs:
  the Redis instance must be access-controlled (no anonymous internet-facing
  Redis).
- **No token material in logs/errors**: store errors log `sandboxID`,
  `backend`, and the operation — never token values.
- **Race safety**: SetNX arbitration prevents two replicas from minting
  competing tokens for one sandbox (which would silently invalidate the
  SDK's copy).
- **Fail-closed**: store build failure (bad config, unreachable Redis) fails
  startup; routing `Set` failure fails sandbox creation rather than leaving an
  unroutable sandbox behind.
- **Constant-time comparison**: `ValidateAccessToken` keeps its current
  string comparison (parity; the token is high-entropy 128-bit random — timing
  attack is not practical). No change.

## 7. Observability

- Structured logs on store errors include `sandboxID`, `backend`, `op`
  (`get_token`/`validate_token`/`routing_set`/...), per repo logging
  conventions.
- Existing `metrics.RecordTokenFailure` keeps firing on validation failures;
  the proxy's new 404-on-miss and 502-on-store-error paths are visible
  through existing HTTP metrics (status label).
- `CountByBackend` continues to feed the sandbox-count gauge (via `Scan` in
  redis mode).

## 8. Testing Strategy

**Unit** (both store implementations run the same contract suite; redis via
`miniredis`, already a dependency):

- Store contract: Get/Set/Delete/TTL expiry/SetNX semantics/Scan.
- Config validation: unknown type, redis without addr, TLS rule, routingTTL
  default.
- Token arbitration: concurrent `GetAccessToken` from two adapter instances
  sharing one store ⇒ both return the same token.
- `ExpiresAt` truthfulness: second `GetAccessToken` call returns the original
  expiry, not a re-stamped one.
- Registry: Set/Get/Delete round-trip through redis store; TTL refreshed on
  Get; `CountByBackend` via Scan.
- envd proxy: miss + single backend ⇒ forwarded; miss + multiple backends ⇒
  404; store error during validate ⇒ 502.
- Cross-replica simulation: two adapter/registry instances over one miniredis
  — create on A, validate + route on B.

**E2E** (new kind job `e2e-multi-replica`, agent-sandbox path):

- Deploy Redis (single-pod manifest) + gateway `replicas: 2` with
  `sessionStore.type: redis`.
- Pin port-forward to pod A: `POST /sandboxes` ⇒ token.
- Pin port-forward to pod B: data-plane op (command run or fs read) with that
  token ⇒ assert NOT 401.
- `kubectl rollout restart` the gateway deployment; after ready, the original
  token still validates a data-plane op.

Gate: `make pre-commit` green; existing E2E jobs unaffected
(`type: memory` default).

## 9. Migration / Rollout

- Default config (`type: memory`) is byte-for-byte behavior-compatible with
  today's single-replica deployments. No migration needed.
- Operators scaling out — TWO-STEP migration (overlay comments say exactly
  this): (1) provision Redis and set `sessionStore.type: redis` while keeping
  `replicas: 1` AND the `Recreate` strategy — during this rollout, old
  in-memory replicas and the new redis-backed replica must never overlap
  (they share no tokens ⇒ nondeterministic 401s), and existing in-memory
  sessions do not survive it (SDKs re-fetch tokens); (2) only after every
  replica is redis-backed, raise replicas / re-enable the HPA and switch to
  RollingUpdate.
- Rolling upgrade note: old replicas (in-memory) and new replicas (shared
  store) do not share tokens — during the upgrade window, pin `replicas: 1`
  or accept transient 401s for sandboxes created before the upgrade. Documented
  in CHANGELOG.
- `SandboxBackendMap`/`NewRegistry` signature change is internal (no exported
  API consumers outside the repo).

## 10. Open Questions

None — config placement (global `sessionStore`), E2E scope (in this PR),
routing TTL policy (24h refresh-on-read), and store abstraction (generic KV
package) were decided during brainstorming.
