# Auth secretRef API-Key Loading (#92) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `auth.providers[].secretRef` (apikey) load real API keys from a JSON file on disk and delete the hardcoded `test-key-1`/`test-key-admin` fallback, failing startup when the file cannot be resolved.

**Architecture:** `secretRef` becomes a filesystem path to a single JSON file mapping API key → tenant context (`tenantID`, `apiKeyID`, `scopes`, `metadata`). On Kubernetes the file comes from a Secret volume mount; off Kubernetes it is a plain file. `NewAPIKeyProvider` gains an error return; `NewManager` propagates it so the gateway fails fast at startup. No Kubernetes API calls, no RBAC changes, no new dependencies.

**Tech Stack:** Go 1.26 (stdlib `os`/`encoding/json`/`sort` only), Helm, Kustomize.

**Spec:** `docs/superpowers/specs/2026-11-16-auth-secretref-api-keys-design.md`

## Global Constraints

- Module: `github.com/e2bgateway/e2bgateway`; branch `fix/auth-secretref-issue-92` (already checked out).
- Every new `.go` file starts with the repo Apache-2.0 license header (copy from `internal/auth/auth.go:1-13`).
- Error messages and logs must NEVER contain API key values, JSON entry key names, or raw file content — the JSON map keys *are* the secrets. Refer to entries by sorted position only ("entry 2 of 3").
- `keys` and `secretRef` on the same apikey provider is a config validation error (no silent precedence).
- No new Go dependencies. No changes to `deploy/**/rbac.yaml`.
- Verification gate for the whole change: `make pre-commit` (fmt + vet + lint + test).
- Conventional-commit messages; reference `(#92)` in each commit subject.

---

### Task 1: Config validation — `keys`/`secretRef` mutual exclusion

**Files:**
- Modify: `internal/config/config.go:289-298` (`AuthProviderConfig.Validate`)
- Test: `internal/config/config_test.go` (append)

**Interfaces:**
- Consumes: existing `AuthProviderConfig` fields `Keys []string`, `SecretRef string`.
- Produces: `Validate()` returns an error for apikey providers that set both `Keys` and `SecretRef`. Task 3's loader may therefore assume at most one is set (but still handles both-empty and keys-first ordering defensively).

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:

```go
func TestValidateAPIKeyProviderKeysSecretRefExclusive(t *testing.T) {
	both := AuthProviderConfig{
		Type:      "apikey",
		Keys:      []string{"k1"},
		SecretRef: "/etc/e2bgateway/auth/keys.json",
	}
	if err := both.Validate(); err == nil {
		t.Error("expected error when both keys and secretRef are set")
	}

	keysOnly := AuthProviderConfig{Type: "apikey", Keys: []string{"k1"}}
	if err := keysOnly.Validate(); err != nil {
		t.Errorf("keys only: unexpected error: %v", err)
	}

	secretOnly := AuthProviderConfig{Type: "apikey", SecretRef: "/tmp/keys.json"}
	if err := secretOnly.Validate(); err != nil {
		t.Errorf("secretRef only: unexpected error: %v", err)
	}

	neither := AuthProviderConfig{Type: "apikey"}
	if err := neither.Validate(); err != nil {
		t.Errorf("neither set: unexpected error: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestValidateAPIKeyProviderKeysSecretRefExclusive -v`
Expected: FAIL — "expected error when both keys and secretRef are set" (current apikey branch returns nil unconditionally).

- [ ] **Step 3: Implement validation**

In `internal/config/config.go`, replace the `case "apikey":` branch of `AuthProviderConfig.Validate()` (currently `return nil` at line 292):

```go
	case "apikey":
		if len(c.Keys) > 0 && c.SecretRef != "" {
			return fmt.Errorf("apikey provider: keys and secretRef are mutually exclusive; set only one")
		}
		return nil
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS (all config tests, including the new one).

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "fix(config): reject apikey providers that set both keys and secretRef (#92)"
```

---

### Task 2: `loadKeysFromSecretFile` loader

**Files:**
- Modify: `internal/auth/auth.go` (add loader + `secretKeyEntry` type after the `APIKeyProvider` section; add `os`, `sort` imports)
- Create: `internal/auth/secretref_test.go` (license header + `package auth`)

**Interfaces:**
- Consumes: `TenantContext` (existing, `internal/auth/auth.go:59-64`).
- Produces: `func loadKeysFromSecretFile(path string) (map[string]*TenantContext, error)` — unexported; used by Task 3's `NewAPIKeyProvider`. Also `func writeKeyFile(t *testing.T, content string) string` test helper reused by Task 3's tests.

- [ ] **Step 1: Write the failing tests**

Create `internal/auth/secretref_test.go` — start with the repo license header (copy verbatim from `internal/auth/auth.go:1-13`), then:

```go
package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeKeyFile writes content to a keys.json file in a fresh temp dir and
// returns its path.
func writeKeyFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing key file: %v", err)
	}
	return path
}

func TestLoadKeysFromSecretFile_Valid(t *testing.T) {
	path := writeKeyFile(t, `{
		"sk-prod-abc": {"tenantID": "acme", "apiKeyID": "key-1", "scopes": ["sandbox:read"], "metadata": {"team": "infra"}},
		"sk-no-scopes": {"tenantID": "solo"}
	}`)

	keys, err := loadKeysFromSecretFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}

	tc := keys["sk-prod-abc"]
	if tc.TenantID != "acme" || tc.APIKeyID != "key-1" {
		t.Errorf("unexpected tenant context: %+v", tc)
	}
	if len(tc.Scopes) != 1 || tc.Scopes[0] != "sandbox:read" {
		t.Errorf("unexpected scopes: %v", tc.Scopes)
	}
	if tc.Metadata["team"] != "infra" {
		t.Errorf("unexpected metadata: %v", tc.Metadata)
	}

	// Omitted scopes must grant nothing (safe default).
	if HasScope(keys["sk-no-scopes"], "sandbox:read") {
		t.Error("expected no scope grants when scopes omitted")
	}
}

func TestLoadKeysFromSecretFile_Errors(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"invalid JSON", `{not json`},
		{"empty object", `{}`},
		{"JSON array", `[{"tenantID": "a"}]`},
		{"null", `null`},
		{"missing tenantID", `{"sk-1": {"scopes": ["*"]}}`},
		{"empty tenantID", `{"sk-1": {"tenantID": ""}}`},
		{"string value", `{"sk-1": "just-a-string"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeKeyFile(t, tt.content)
			if _, err := loadKeysFromSecretFile(path); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestLoadKeysFromSecretFile_MissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.json")
	if _, err := loadKeysFromSecretFile(missing); err == nil {
		t.Error("expected error for missing file")
	}
}

// Errors must not leak secret material: the JSON map keys ARE the API keys.
func TestLoadKeysFromSecretFile_NoSecretMaterialInError(t *testing.T) {
	path := writeKeyFile(t, `{"sk-super-secret-value": {"scopes": ["*"]}}`)
	_, err := loadKeysFromSecretFile(path)
	if err == nil {
		t.Fatal("expected error (missing tenantID)")
	}
	if strings.Contains(err.Error(), "sk-super-secret-value") {
		t.Errorf("error must not contain key material, got: %v", err)
	}
	if strings.Contains(err.Error(), "sandbox") {
		t.Errorf("error must not contain file content, got: %v", err)
	}
}
```

Note: `{"sk-1": "just-a-string"}` fails `json.Unmarshal` into the typed map (cannot unmarshal string into `secretKeyEntry`) — that is the desired rejection.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/auth/ -run TestLoadKeysFromSecretFile -v`
Expected: compile FAIL — `undefined: loadKeysFromSecretFile`.

- [ ] **Step 3: Implement the loader**

In `internal/auth/auth.go`:

Add `os` and `sort` to the import block (keep alphabetical order within the stdlib group):

```go
import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/config"
)
```

Insert after the `RemoveKey` method (before the `--- Rate Limiter ---` comment):

```go
// --- secretRef file loading ---

// secretKeyEntry is one API key's tenant context as stored in a secretRef
// JSON file. The JSON map key (not a field) holds the API key value itself.
type secretKeyEntry struct {
	TenantID string            `json:"tenantID"`
	APIKeyID string            `json:"apiKeyID,omitempty"`
	Scopes   []string          `json:"scopes,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// loadKeysFromSecretFile reads API keys from a JSON file that maps
// API key value -> tenant context. It works with any file on disk; on
// Kubernetes the file is typically a Secret volume mount. Error messages
// never include key values or raw file content, because the JSON map keys
// are the secrets themselves.
func loadKeysFromSecretFile(path string) (map[string]*TenantContext, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading secretRef file: %w", err)
	}

	var entries map[string]secretKeyEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parsing secretRef file %s: must be a JSON object mapping API key -> {tenantID, apiKeyID, scopes, metadata}", path)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("secretRef file %s contains no API keys", path)
	}

	// Validate in sorted order so errors are deterministic; refer to entries
	// by position, never by name (the name is the API key).
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	keys := make(map[string]*TenantContext, len(entries))
	for i, name := range names {
		entry := entries[name]
		if entry.TenantID == "" {
			return nil, fmt.Errorf("secretRef file %s: entry %d of %d: tenantID is required", path, i+1, len(names))
		}
		keys[name] = &TenantContext{
			TenantID: entry.TenantID,
			APIKeyID: entry.APIKeyID,
			Scopes:   entry.Scopes,
			Metadata: entry.Metadata,
		}
	}
	return keys, nil
}
```

(`json.Unmarshal` of `null` leaves `entries` nil → caught by the `len == 0` check. A JSON array fails the typed unmarshal → caught by the parse error.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/auth/ -run TestLoadKeysFromSecretFile -v`
Expected: PASS (all subtests, including NoSecretMaterialInError).

- [ ] **Step 5: Commit**

```bash
git add internal/auth/auth.go internal/auth/secretref_test.go
git commit -m "feat(auth): add secretRef JSON key-file loader (#92)"
```

---

### Task 3: Wire the loader in, delete hardcoded fallback, fix existing tests

**Files:**
- Modify: `internal/auth/auth.go` — `NewAPIKeyProvider` (lines 143-173), `NewManager` apikey branch (lines 85-87), const block (lines 35-41)
- Modify: `internal/auth/auth_test.go` — `TestAPIKeyProvider_Authenticate` (35), `TestAPIKeyProvider_BearerAuth` (71), `TestAPIKeyProvider_AddRemoveKey` (92), `TestManager_Authenticate` (355)
- Modify: `internal/server/middleware/middleware_test.go` — `TestAuthMiddleware_ValidKey` (~46-75), `TestAuthMiddleware_InvalidKey` (~77-99)

**Interfaces:**
- Consumes: `loadKeysFromSecretFile(path string) (map[string]*TenantContext, error)` and `writeKeyFile(t, content)` from Task 2.
- Produces: `func NewAPIKeyProvider(cfg config.AuthProviderConfig) (*APIKeyProvider, error)` — all callers (only `NewManager` in non-test code) must handle the error. `NewManager` signature unchanged: `(*Manager, error)`.

- [ ] **Step 1: Update/extend the failing tests first**

In `internal/auth/auth_test.go`:

1a. Replace `TestAPIKeyProvider_Authenticate` (the old one relied on the hardcoded fallback via `SecretRef: "test-secret"` + `test-key-1`):

```go
func TestAPIKeyProvider_Authenticate(t *testing.T) {
	p, err := NewAPIKeyProvider(config.AuthProviderConfig{
		Type:       "apikey",
		Keys:       []string{"valid-key"},
		HeaderName: "X-API-Key",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Test with valid key
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "valid-key")

	tc, err := p.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if tc.TenantID != "tenant-0" {
		t.Errorf("expected tenant 'tenant-0', got %s", tc.TenantID)
	}

	// Test with invalid key
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("X-API-Key", "invalid-key")

	_, err = p.Authenticate(req2)
	if err == nil {
		t.Error("expected error for invalid key")
	}

	// Test with missing key
	req3 := httptest.NewRequest(http.MethodGet, "/", nil)
	_, err = p.Authenticate(req3)
	if err == nil {
		t.Error("expected error for missing key")
	}
}
```

1b. Replace `TestAPIKeyProvider_BearerAuth`:

```go
func TestAPIKeyProvider_BearerAuth(t *testing.T) {
	p, err := NewAPIKeyProvider(config.AuthProviderConfig{
		Type:            "apikey",
		Keys:            []string{"valid-key"},
		HeaderName:      "X-API-Key",
		AlternateHeader: "Authorization",
		BearerPrefix:    true,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer valid-key")

	tc, err := p.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if tc.TenantID != "tenant-0" {
		t.Errorf("expected tenant 'tenant-0', got %s", tc.TenantID)
	}
}
```

1c. In `TestAPIKeyProvider_AddRemoveKey`, change the constructor call to handle the new error return (rest of the test unchanged):

```go
	p, err := NewAPIKeyProvider(config.AuthProviderConfig{
		Type:       "apikey",
		HeaderName: "X-API-Key",
	})
	if err != nil {
		t.Fatal(err)
	}
```

1d. Replace `TestManager_Authenticate` (exercises secretRef end-to-end through the Manager; uses `writeKeyFile` from `secretref_test.go`, same package):

```go
func TestManager_Authenticate(t *testing.T) {
	path := writeKeyFile(t, `{"mgr-key": {"tenantID": "mgr-tenant", "scopes": ["*"]}}`)
	mgr, err := NewManager(config.AuthConfig{
		Providers: []config.AuthProviderConfig{
			{Type: "apikey", SecretRef: path, HeaderName: "X-API-Key"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Valid key
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "mgr-key")

	tc, err := mgr.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if tc.TenantID != "mgr-tenant" {
		t.Errorf("expected tenant 'mgr-tenant', got %s", tc.TenantID)
	}

	// Invalid key
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("X-API-Key", "wrong")
	_, err = mgr.Authenticate(req2)
	if err == nil {
		t.Error("expected error for invalid key")
	}
}
```

1e. Append new tests (issue #92 core acceptance) to `internal/auth/secretref_test.go` — add `"net/http"`, `"net/http/httptest"`, and the config import to its import block:

```go
func TestNewAPIKeyProvider_SecretRef(t *testing.T) {
	path := writeKeyFile(t, `{"sk-file-key": {"tenantID": "file-tenant", "apiKeyID": "key-f", "scopes": ["sandbox:read"]}}`)
	p, err := NewAPIKeyProvider(config.AuthProviderConfig{
		Type:       "apikey",
		SecretRef:  path,
		HeaderName: "X-API-Key",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "sk-file-key")
	tc, err := p.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if tc.TenantID != "file-tenant" || tc.APIKeyID != "key-f" {
		t.Errorf("unexpected tenant context: %+v", tc)
	}
	if !HasScope(tc, "sandbox:read") {
		t.Error("expected sandbox:read scope from file")
	}
}

// Fail fast: secretRef set but unresolvable must be a startup error.
func TestNewManager_SecretRefUnresolvable(t *testing.T) {
	_, err := NewManager(config.AuthConfig{
		Providers: []config.AuthProviderConfig{
			{Type: "apikey", SecretRef: "/nonexistent/keys.json"},
		},
	})
	if err == nil {
		t.Fatal("expected error for unresolvable secretRef")
	}
}

// Regression for #92: secretRef must never enable the legacy hardcoded keys.
func TestAPIKeyProvider_NoHardcodedFallback(t *testing.T) {
	path := writeKeyFile(t, `{"sk-real": {"tenantID": "real-tenant"}}`)
	p, err := NewAPIKeyProvider(config.AuthProviderConfig{
		Type:      "apikey",
		SecretRef: path,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, legacy := range []string{"test-key-1", "test-key-admin"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-API-Key", legacy)
		if _, err := p.Authenticate(req); err == nil {
			t.Errorf("legacy hardcoded key %q must not be accepted", legacy)
		}
	}
}
```

1f. In `internal/server/middleware/middleware_test.go`, both `TestAuthMiddleware_ValidKey` and `TestAuthMiddleware_InvalidKey` build the Manager with `{Type: "apikey", SecretRef: "test", HeaderName: "X-API-Key"}` — replace that provider literal in **both** tests with:

```go
			{Type: "apikey", Keys: []string{"mw-key"}, HeaderName: "X-API-Key"},
```

In `TestAuthMiddleware_ValidKey` also:
- change `req.Header.Set("X-API-Key", "test-key-1")` → `req.Header.Set("X-API-Key", "mw-key")`
- change the tenant expectation `if capturedTenant != "test-tenant"` → `if capturedTenant != "tenant-0"` and the error message strings `'test-tenant'` → `'tenant-0'`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/auth/ ./internal/server/middleware/ -v 2>&1 | tail -20`
Expected: compile FAIL — `NewAPIKeyProvider` returns 1 value but tests assign 2 (and vice versa once implementation lands); this confirms the signature change is required.

- [ ] **Step 3: Implement**

In `internal/auth/auth.go`:

3a. Delete the now-unused constants from the const block — remove these two lines (lines 39-40):

```go
	scopeSandboxRead    = "sandbox:read"
	testTenantID        = "test-tenant"
```

(Keep `providerTypeAPIKey`, `providerTypeJWT`, `defaultAPIKeyHeader`. Both removed constants were only used by the hardcoded fallback.)

3b. Replace `NewAPIKeyProvider` (lines 142-173) entirely:

```go
// NewAPIKeyProvider creates a new API key provider. Keys come from the static
// cfg.Keys list or, when cfg.SecretRef is set, from the JSON file at that path
// (see loadKeysFromSecretFile). An unresolvable secretRef is an error — the
// caller (NewManager) fails startup rather than serving without the operator's
// configured keys.
func NewAPIKeyProvider(cfg config.AuthProviderConfig) (*APIKeyProvider, error) {
	p := &APIKeyProvider{
		cfg:  cfg,
		keys: make(map[string]*TenantContext),
	}

	switch {
	case len(cfg.Keys) > 0:
		// Load static keys from config
		for i, key := range cfg.Keys {
			p.keys[key] = &TenantContext{
				TenantID: fmt.Sprintf("tenant-%d", i),
				APIKeyID: fmt.Sprintf("key-%d", i),
				Scopes:   []string{"*"},
			}
		}
	case cfg.SecretRef != "":
		keys, err := loadKeysFromSecretFile(cfg.SecretRef)
		if err != nil {
			return nil, fmt.Errorf("loading secretRef API keys: %w", err)
		}
		p.keys = keys
	}

	return p, nil
}
```

3c. In `NewManager`, replace the apikey branch (lines 85-87):

```go
		case providerTypeAPIKey:
			p, err := NewAPIKeyProvider(pCfg)
			if err != nil {
				return nil, fmt.Errorf("configuring apikey provider: %w", err)
			}
			m.providers = append(m.providers, p)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/auth/ ./internal/server/middleware/ ./internal/config/ -v 2>&1 | tail -30`
Expected: PASS — all auth, middleware, and config tests green.

Run: `go build ./... && go vet ./...`
Expected: no output (the only non-test caller, `internal/server/http.go:65`, already handles `NewManager`'s error).

- [ ] **Step 5: Verify no hardcoded-key references survive**

Run: `grep -rn "test-key-1\|test-key-admin\|testTenantID\|scopeSandboxRead" --include="*.go" .`
Expected: only `middleware_test.go` hits if any remain — there must be NONE outside `docs/`. (`mw-key`/`valid-key` replaced them.)

- [ ] **Step 6: Commit**

```bash
git add internal/auth/ internal/server/middleware/middleware_test.go
git commit -m "fix(auth)!: load apikey secretRef from JSON file, remove hardcoded test keys (#92)

BREAKING CHANGE: secretRef is now a path to a JSON file mapping API key ->
{tenantID, apiKeyID, scopes, metadata}. An unresolvable secretRef fails
startup instead of silently enabling test-key-1/test-key-admin."
```

---

### Task 4: Helm chart — wire `existingSecret`, update docs

**Files:**
- Modify: `deploy/helm/e2bgateway/templates/deployment.yaml` (volumeMounts ~line 63, volumes ~line 67)
- Modify: `deploy/helm/e2bgateway/values.yaml` (auth section ~line 162, `existingSecret` ~line 198)
- Modify: `deploy/helm/e2bgateway/README.md` (line ~128 `existingSecret` row, lines ~229-239 NOTE block)

**Interfaces:**
- Consumes: Task 3's secretRef semantics (path to JSON file).
- Produces: when `.Values.existingSecret` is non-empty, the Secret is mounted read-only at `/etc/e2bgateway/auth`, so `secretRef: /etc/e2bgateway/auth/keys.json` resolves.

- [ ] **Step 1: deployment.yaml — conditional volume + mount**

Replace the existing `volumeMounts:`/`volumes:` block:

```yaml
          volumeMounts:
            - name: config
              mountPath: /etc/e2bgateway
              readOnly: true
            {{- if .Values.existingSecret }}
            - name: api-keys
              mountPath: /etc/e2bgateway/auth
              readOnly: true
            {{- end }}
      volumes:
        - name: config
          configMap:
            name: {{ include "e2bgateway.fullname" . }}
        {{- if .Values.existingSecret }}
        - name: api-keys
          secret:
            secretName: {{ .Values.existingSecret }}
        {{- end }}
```

- [ ] **Step 2: values.yaml — auth example + existingSecret docs**

Replace the `auth:` block under `config:` (keeping the existing JWT comment block that follows it intact):

```yaml
  auth:
    providers:
      # API-key auth. Either inline `keys` (dev/testing only) or `secretRef`
      # pointing at a JSON file — on Kubernetes, mount a Secret via
      # `existingSecret` below. Setting both is a startup error.
      - type: apikey
        keys:
          - "test-key"
      # Production example (requires existingSecret: e2bgw-api-keys):
      # - type: apikey
      #   secretRef: /etc/e2bgateway/auth/keys.json
      #   headerName: X-API-Key
```

Replace the `existingSecret` comment + value:

```yaml
# Existing Secret with API keys, mounted read-only at /etc/e2bgateway/auth.
# The Secret must contain a `keys.json` data key whose value is a JSON object
# mapping API key -> {"tenantID": "...", "apiKeyID": "...", "scopes": [...],
# "metadata": {...}} (tenantID required; scopes default to none). Then set
# auth.providers[].secretRef to /etc/e2bgateway/auth/keys.json.
# Key rotation: kubelet syncs the mount; restart the gateway to reload.
existingSecret: ""
```

- [ ] **Step 3: README.md**

3a. Replace the `existingSecret` table row (line ~128):

```markdown
| `existingSecret` | Existing Secret with API keys; mounted read-only at `/etc/e2bgateway/auth`. Must contain a `keys.json` data key (JSON object mapping API key → `{tenantID, apiKeyID, scopes, metadata}`). Use with `auth.providers[].secretRef: /etc/e2bgateway/auth/keys.json` | `""` |
```

3b. Replace the NOTE block in the values example (lines ~229-239):

```markdown
  auth:
    providers:
      # Either inline `keys` (dev) or `secretRef` (a JSON file path — mount a
      # Secret via `existingSecret`). Setting both is a startup error.
      - type: apikey
        keys:
          - "production-api-key-1"
          - "production-api-key-2"
        headerName: "X-API-Key"
      # With existingSecret: e2bgw-api-keys (Secret data key `keys.json`):
      # - type: apikey
      #   secretRef: /etc/e2bgateway/auth/keys.json
      #   headerName: "X-API-Key"
```

- [ ] **Step 4: Verify rendering both ways**

Run: `helm template test deploy/helm/e2bgateway 2>/dev/null | grep -c "api-keys"` → expected `0`.
Run: `helm template test deploy/helm/e2bgateway --set existingSecret=e2bgw-api-keys | grep -B1 -A3 "api-keys"` → expected: volumeMount at `/etc/e2bgateway/auth` and `secretName: e2bgw-api-keys`.
Run: `helm lint deploy/helm/e2bgateway` → expected: passing.
(If `helm` is not installed: `brew install helm` or note it and rely on Step 5's YAML sanity + Task 6 review.)

- [ ] **Step 5: Commit**

```bash
git add deploy/helm/
git commit -m "feat(deploy): mount existingSecret for apikey secretRef in helm chart (#92)"
```

---

### Task 5: Kustomize, example configs, CHANGELOG

**Files:**
- Modify: `deploy/kustomize/base/configmap.yaml` (auth section ~lines 90-101)
- Modify: `deploy/kustomize/base/deployment.yaml` (volumeMounts/volumes ~lines 79-87)
- Modify: `configs/e2bgateway-default.yaml` (auth section ~lines 44-49)
- Modify: `configs/e2bgateway-example.yaml` (auth section ~lines 96-103)
- Modify: `CHANGELOG.md` (Unreleased section)

**Interfaces:**
- Consumes: Task 3 semantics; Task 4's mount-path convention (`/etc/e2bgateway/auth/keys.json`) reused for consistency.
- Produces: docs/manifests that match the implemented behavior; `make run` (which uses `configs/e2bgateway-default.yaml`, see `Makefile:32`) stays functional for local dev.

- [ ] **Step 1: kustomize configmap.yaml — replace the disabled-secretRef NOTE**

Replace the auth providers block (the NOTE comment through the `# secretRef:` line):

```yaml
    auth:
      providers:
        # API-key auth: inline `keys` (dev) or `secretRef` pointing at a JSON
        # file (production). Setting both is a startup error. The file maps
        # API key -> {"tenantID", "apiKeyID", "scopes", "metadata"}; on
        # Kubernetes mount a Secret via the commented volume example in
        # deployment.yaml. Format details: configs/e2bgateway-example.yaml.
        - type: apikey
          keys:
            - "REPLACE-ME-api-key"
          headerName: "X-API-Key"
          # secretRef: "/etc/e2bgateway/auth/keys.json"
```

- [ ] **Step 2: kustomize deployment.yaml — commented Secret volume example**

Replace the volumeMounts/volumes block:

```yaml
          volumeMounts:
            - name: config
              mountPath: /etc/e2bgateway
              readOnly: true
            # Requires the Secret volume below and
            # secretRef: /etc/e2bgateway/auth/keys.json in the configmap.
            # - name: api-keys
            #   mountPath: /etc/e2bgateway/auth
            #   readOnly: true
      volumes:
        - name: config
          configMap:
            name: e2bgateway-config
        # Secret data key `keys.json`: JSON object mapping API key ->
        # {"tenantID", "apiKeyID", "scopes", "metadata"} (tenantID required).
        # - name: api-keys
        #   secret:
        #     secretName: e2bgw-api-keys
```

- [ ] **Step 3: configs/e2bgateway-default.yaml — keep `make run` working**

The current `secretRef: "e2bgw-api-keys"` would now fail startup (no such file). Replace the auth provider block with an explicit local-dev key plus a commented secretRef example:

```yaml
# Authentication
auth:
  providers:
    - type: apikey
      # Local development only — replace with secretRef in real deployments.
      keys:
        - "dev-local-key"
      headerName: "X-API-Key"
      # secretRef is a path to a JSON file mapping API key ->
      # {"tenantID", "apiKeyID", "scopes", "metadata"}. On Kubernetes, mount a
      # Secret as a volume (see deploy/); elsewhere, any file on disk works.
      # Unresolvable secretRef fails startup. Cannot be combined with keys.
      # secretRef: "/etc/e2bgateway/auth/keys.json"
```

- [ ] **Step 4: configs/e2bgateway-example.yaml — path-based secretRef + format docs**

Replace the apikey provider entry (the one with `secretRef: "e2bgw-api-keys"`):

```yaml
auth:
  providers:
    # API-key auth. Exactly one of `keys` (inline, dev/testing) or
    # `secretRef` (path to a JSON key file) — setting both is an error.
    #
    # secretRef file format (one JSON object; map key = API key value):
    #   {
    #     "sk-prod-abc123": {
    #       "tenantID": "acme",            # required
    #       "apiKeyID": "key-acme-1",      # optional
    #       "scopes": ["sandbox:read"],    # optional; omitted = no scopes
    #       "metadata": {"team": "infra"}  # optional
    #     }
    #   }
    # Kubernetes: put the JSON in a Secret data key (e.g. keys.json), mount
    # the Secret as a volume, and point secretRef at the mount path (see
    # deploy/helm `existingSecret` or deploy/kustomize deployment.yaml).
    # Non-Kubernetes: any file on disk (Docker bind mount, VM, bare metal).
    # The file is read once at startup; restart to pick up rotations.
    - type: apikey
      secretRef: "/etc/e2bgateway/auth/keys.json"
      headerName: "X-API-Key"
      alternateHeader: "Authorization"
      bearerPrefix: true
```

- [ ] **Step 5: CHANGELOG.md — Unreleased entry**

Add under `## Unreleased` (above the existing metrics entry):

```markdown
* [BUGFIX] **Breaking (security):** `auth.providers[].secretRef` (apikey) now loads API keys from a JSON file at the given path (Kubernetes: mount a Secret as a volume — see the helm `existingSecret` value; non-Kubernetes: any file on disk). The hardcoded `test-key-1`/`test-key-admin` fallback is removed — an unresolvable `secretRef` now fails startup, and setting both `keys` and `secretRef` is a config error. #92
```

- [ ] **Step 6: Verify**

Run: `kubectl kustomize deploy/kustomize/base >/dev/null && echo OK` (or `kustomize build deploy/kustomize/base`) → expected `OK`.
Run: `go run ./cmd/e2bgateway --config configs/e2bgateway-default.yaml --help >/dev/null 2>&1 || true; grep -rn "e2bgw-api-keys" configs/ deploy/` → expected: only commented/doc references remain, none active.
Run: `grep -rn "NOT wired" deploy/ configs/ docs/api 2>/dev/null` → expected: no output.

- [ ] **Step 7: Commit**

```bash
git add deploy/kustomize/ configs/ CHANGELOG.md
git commit -m "docs(deploy): align manifests and config examples with secretRef file loading (#92)"
```

---

### Task 6: Full verification gate

**Files:** none (verification only; fix-forward commits if anything fails)

**Interfaces:**
- Consumes: everything from Tasks 1-5.
- Produces: a green `make pre-commit` on the branch — the repo's required PR gate.

- [ ] **Step 1: Run the full gate**

Run: `make pre-commit`
Expected: fmt, vet, lint (golangci-lint), and the full test suite all pass. If lint flags the new code (e.g. import order, unused constants), fix and re-run.

- [ ] **Step 2: Manual smoke test of fail-fast + happy path**

```bash
# Fail fast on bad secretRef
cat > /tmp/e2bgw-smoke.yaml <<'EOF'
server:
  http:
    address: "127.0.0.1:18080"
backends:
  - name: mock
    type: mock
    enabled: true
auth:
  providers:
    - type: apikey
      secretRef: "/tmp/does-not-exist.json"
EOF
go run ./cmd/e2bgateway --config /tmp/e2bgw-smoke.yaml; echo "exit=$?"
# Expected: startup error mentioning the secretRef path, exit != 0

# Happy path: valid key file
echo '{"sk-smoke": {"tenantID": "smoke-tenant", "scopes": ["*"]}}' > /tmp/e2bgw-keys.json
sed -i '' 's#/tmp/does-not-exist.json#/tmp/e2bgw-keys.json#' /tmp/e2bgw-smoke.yaml
go run ./cmd/e2bgateway --config /tmp/e2bgw-smoke.yaml &>/tmp/e2bgw-smoke.log &
GW_PID=$!
sleep 3
curl -sf -H "X-API-Key: sk-smoke" http://127.0.0.1:18080/api/v1/sandboxes -o /dev/null && echo "AUTH OK"
curl -s -o /dev/null -w "%{http_code}\n" -H "X-API-Key: test-key-1" http://127.0.0.1:18080/api/v1/sandboxes   # expect 401
kill $GW_PID
rm -f /tmp/e2bgw-smoke.yaml /tmp/e2bgw-keys.json /tmp/e2bgw-smoke.log
```

Expected: `AUTH OK` for the file-loaded key; `401` for the legacy hardcoded key.
(If the mock backend's sandboxes route needs a tenant header/other setup, a 401 vs 200 distinction on the two curl calls is the assertion that matters.)

- [ ] **Step 3: Final diff review + confirm branch state**

Run: `git status --short` → expected: clean.
Run: `git log --oneline main..HEAD` → expected: 6 commits (spec, Tasks 1-5).

- [ ] **Step 4 (only if fixes were needed): Commit**

```bash
git add -A
git commit -m "fix(auth): address pre-commit findings for secretRef loading (#92)"
```
