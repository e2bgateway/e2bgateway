# Auth: Load API Keys from secretRef File

**Issue**: #92 — auth: apikey provider secretRef does not load the referenced Secret and silently falls back to hardcoded test keys
**Date**: 2026-11-16
**Priority**: P0 (security)
**Approach**: A — Mounted-file secretRef + `NewAPIKeyProvider` returns error, fail fast at startup

---

## 1. Problem Statement

`NewAPIKeyProvider` (`internal/auth/auth.go:143-173`) ignores the Kubernetes Secret named by
`auth.providers[].secretRef`. When `keys` is empty and `secretRef` is set, it seeds two hardcoded
keys instead:

- `test-key-1` (sandbox read/write + template read scopes)
- `test-key-admin` (scope `*`)

Deployments that configure `secretRef` (as `configs/e2bgateway-example.yaml` and
`configs/e2bgateway-default.yaml` suggest) do not accept the keys stored in the Secret — worse,
they accept **published static credentials, including an admin-scoped one**. An operator who
believes they configured real keys has actually enabled known test keys.

## 2. Design Goals

- `secretRef` resolves to real key material at startup, or the gateway refuses to start.
- Remove the hardcoded `test-key-1` / `test-key-admin` fallback entirely.
- Work identically on Kubernetes (Secret mounted as a volume) and **off Kubernetes**
  (Docker, VM, bare metal — a plain file on disk). No Kubernetes API dependency, no extra RBAC.
- Per-key tenant identity and scopes are expressible (not just bare key strings).
- Update `deploy/` manifests and `configs/` examples to match the fixed semantics.

**Non-goals (this iteration):**

- Hot reload of the key file (rotation is picked up on restart; runtime mutation remains possible
  via the existing `AddKey` / `RemoveKey` methods).
- Reading Secrets via the in-cluster Kubernetes API (explicitly rejected: extra RBAC surface,
  Kubernetes-only).
- A pluggable `KeyStore` abstraction (YAGNI — one implementation exists).

## 3. Architecture

### 3.1 Interface changes

```go
// internal/auth/auth.go
// Before: func NewAPIKeyProvider(cfg config.AuthProviderConfig) *APIKeyProvider
// After:
func NewAPIKeyProvider(cfg config.AuthProviderConfig) (*APIKeyProvider, error)

// New unexported loader:
func loadKeysFromSecretFile(path string) (map[string]*TenantContext, error)
```

`NewManager` propagates the error from the `apikey` branch (mirroring the existing `jwt` branch
pattern). `internal/server/http.go:65` already handles `NewManager` errors — startup fails fast
with no server-side changes.

### 3.2 Loading behavior

`NewAPIKeyProvider` resolution order:

1. `cfg.Keys` non-empty → load static keys from config (unchanged behavior).
2. `cfg.SecretRef` non-empty → `loadKeysFromSecretFile(cfg.SecretRef)`.
3. Both set → rejected earlier by config validation (see §5).
4. Neither set → provider with zero keys (every request fails auth; unchanged).

`loadKeysFromSecretFile`:

- `os.ReadFile(path)` — path is a regular file.
- `json.Unmarshal` into `map[string]secretKeyEntry` where:

```go
type secretKeyEntry struct {
    TenantID string            `json:"tenantID"`
    APIKeyID string            `json:"apiKeyID,omitempty"`
    Scopes   []string          `json:"scopes,omitempty"`
    Metadata map[string]string `json:"metadata,omitempty"`
}
```

- Per-entry validation: `tenantID` must be non-empty. `scopes` defaults to empty (no scope
  grants — safe default). `apiKeyID` and `metadata` are optional.
- Empty map (`{}`) → error: an operator pointing `secretRef` at a key-less file has almost
  certainly misconfigured something.
- Convert each entry to `*TenantContext` keyed by the API key value (the JSON map key).

### 3.3 Data flow

```
startup
  └─ config.Load ──► AuthProviderConfig.Validate ──► keys/secretRef mutual exclusion
  └─ server.New ──► auth.NewManager
                      └─ NewAPIKeyProvider
                           └─ loadKeysFromSecretFile(secretRef)   # regular file read
                                ├─ OK  ──► keys map populated
                                └─ ERR ──► NewManager error ──► gateway exits (fail fast)

Kubernetes:  Secret e2bgw-api-keys ──(volume mount, kubelet-synced)──► /etc/e2bgateway/auth/keys.json
Non-K8s:     operator-managed file on disk at the configured path
```

The gateway process only ever sees a file path; how the file gets there (Secret volume, Docker
bind mount, configuration management) is an operator/deployment concern.

## 4. API Surface

No HTTP API changes. `AddKey` / `RemoveKey` remain available for runtime key management.

## 5. Configuration

`auth.providers[].secretRef` (apikey provider) changes meaning:

- **Before**: (nominally) a Kubernetes Secret name; never actually read.
- **After**: a **filesystem path to a single JSON file**.

File format — top-level JSON object mapping API key value → tenant context:

```json
{
  "sk-prod-abc123": {
    "tenantID": "acme",
    "apiKeyID": "key-acme-1",
    "scopes": ["sandbox:read", "sandbox:write"],
    "metadata": {"team": "infra"}
  },
  "sk-admin-xyz": {
    "tenantID": "admin-tenant",
    "apiKeyID": "key-admin",
    "scopes": ["*"]
  }
}
```

Validation rules added to `AuthProviderConfig.Validate()` (`internal/config/config.go`):

- `type: apikey` with **both** `keys` and `secretRef` set → validation error. Silent precedence
  is exactly the failure mode that caused #92; conflicts must be explicit.

No new fields, no env var changes. (Viper map-key lowercasing note: `secretRef` is a struct
field with a `mapstructure` tag, not a map key — unaffected.)

Example config updates:

- `configs/e2bgateway-default.yaml`: remove `secretRef: "e2bgw-api-keys"` (post-fix it would
  fail startup — the file does not exist); replace with a commented path example.
- `configs/e2bgateway-example.yaml`: `secretRef` becomes a file-path example
  (`/etc/e2bgateway/auth/keys.json`) with format comments covering both K8s and non-K8s usage.

## 6. Security Considerations

- **Removal of static credentials**: `test-key-1` / `test-key-admin` and the `testTenantID`
  constant are deleted. A regression test asserts these keys are never accepted.
- **Fail fast**: unreadable file, invalid JSON, empty object, or an entry missing `tenantID`
  aborts startup with an error naming the `secretRef` path and the structural problem.
- **No secret material in logs/errors**: error messages include the file path and the structural
  problem (e.g. "entry 2 of N: tenantID is required", counting in sorted-key order) but never
  key values, entry key names, or raw file content — the JSON map keys *are* the API keys.
- **Safe scope default**: entries without `scopes` grant no scope-protected operations.
- **No RBAC expansion**: the gateway never calls the Kubernetes API for auth; existing
  `deploy/` RBAC is unchanged.
- **Key rotation**: kubelet syncs mounted Secret files; the gateway picks up changes on restart.
  Documented in deploy manifests/examples.
- Lookup remains a map hit on the exact key string (existing behavior); `CompareKeys`
  constant-time helper is untouched.

## 7. Observability

- Startup failure surfaces through the existing fatal-error path in `internal/cmd` /
  `internal/server` with a wrapped error chain (`configuring apikey provider: loading secretRef
  file <path>: ...`).
- No new metrics. No key material in structured log fields.

## 8. Testing Strategy

Unit tests (`internal/auth/`, using `t.TempDir()` for key files):

- Happy path: valid file → keys loaded, `Authenticate` succeeds with correct TenantContext
  (tenantID, scopes, metadata).
- Missing file → error.
- Invalid JSON → error.
- Empty object `{}` → error.
- Entry missing `tenantID` → error.
- Entry without `scopes` → authenticates but `HasScope` returns false.
- Regression: `test-key-1` / `test-key-admin` are not accepted when `secretRef` is set to a
  valid file, and constructing a provider with only `secretRef` pointing at a missing file
  fails instead of silently succeeding.
- Config validation: apikey provider with both `keys` and `secretRef` → `Validate()` error.

Existing `auth_test.go` cases that relied on the hardcoded fallback (secretRef + `test-key-1`)
are rewritten to use explicit `Keys` config or real temp key files.

Gate: `make pre-commit` (fmt + vet + lint + test) green.

## 9. Migration / Rollout

**Breaking change** for anyone who set `secretRef` — but only in the good direction: their
config never worked as documented, and it was silently insecure. Post-fix behavior:

- `secretRef` pointing at a nonexistent path → startup failure with a clear error (previously:
  silent static test keys).
- Operators must create the Secret/file in the new JSON format.

Deploy manifest changes (same PR):

- **Helm**: wire the existing-but-unused `existingSecret` value — when non-empty, mount it as a
  read-only volume at `/etc/e2bgateway/auth`; document `secretRef:
  /etc/e2bgateway/auth/keys.json` and the Secret data-key format in `values.yaml` /
  `README.md`; remove the "NOT wired yet" notes.
- **Kustomize**: replace the `configmap.yaml` warning NOTE with a working commented `secretRef`
  path example; add a commented Secret volume/volumeMount example in `deployment.yaml`
  documenting the `keys.json` data key. No real keys committed.
- **CHANGELOG**: note the `secretRef` semantic change under Unreleased.

## 10. Open Questions

None — mechanism (mounted file), format (single-file JSON map), and fallback policy (delete +
fail fast) were all decided during brainstorming.
