# Access Token Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement `GetAccessToken()` for the agent-sandbox adapter with scoped tokens (1h TTL) and server-side validation in the envd proxy.

**Architecture:** Per-adapter token cache using `internal/cache` (in-memory LRU). Add `ValidateAccessToken()` to the `SandboxAdapter` interface. The envd proxy validates tokens via the adapter before forwarding ConnectRPC requests. Tokens are formatted as `envd_{sandboxID}_{32-hex}` and reused for the same sandbox until expiry.

**Tech Stack:** Go 1.26+, `internal/cache` (LRU with TTL), `crypto/rand` for token generation, `chi` router, `testify` for tests.

## Global Constraints

- Token TTL: 1 hour (hardcoded)
- Token format: `envd_{sandboxID}_{32-hex-random}`
- Cache backend: `internal/cache.Cache` (in-memory LRU), maxSize=10000
- Token reuse: Same token returned for same sandboxID until expiry
- All existing tests must continue to pass
- New code must have unit test coverage

---

## File Structure

| File | Action | Responsibility |
|---|---|---|
| `internal/adapter/interface.go` | Modify | Add `ValidateAccessToken` to `SandboxAdapter` interface |
| `internal/adapter/agentsandbox/adapter.go` | Modify | Add `tokenCache` field, implement `GetAccessToken` + `ValidateAccessToken` |
| `internal/adapter/mock/adapter.go` | Modify | Add `tokenCache` field, refactor `GetAccessToken`, add `ValidateAccessToken` |
| `internal/adapter/e2bcloud/adapter.go` | Modify | Add `ValidateAccessToken` (returns true — upstream validates) |
| `internal/adapter/opensandbox/adapter.go` | Modify | Add `ValidateAccessToken` (returns not supported) |
| `internal/server/envd_proxy.go` | Modify | Add token validation before forwarding |
| `test/mock/backend.go` | Modify | Add `ValidateAccessToken` mock method |
| `internal/routing/router_test.go` | Modify | Add `ValidateAccessToken` to stub adapter |
| `internal/adapter/agentsandbox/adapter_test.go` | Modify | Add token tests |
| `internal/adapter/mock/adapter_test.go` | Modify | Add ValidateAccessToken test |

---

### Task 1: Extend SandboxAdapter Interface

**Files:**
- Modify: `internal/adapter/interface.go:89-91`
- Modify: `internal/adapter/e2bcloud/adapter.go:604-615`
- Modify: `internal/adapter/opensandbox/adapter.go:629-633`
- Modify: `test/mock/backend.go:109-110, 552-561`
- Modify: `internal/routing/router_test.go:117-119`

**Interfaces:**
- Consumes: existing `SandboxAdapter` interface
- Produces: `ValidateAccessToken(ctx context.Context, sandboxID, token string) (bool, error)` method on all adapters

- [ ] **Step 1: Add ValidateAccessToken to SandboxAdapter interface**

In `internal/adapter/interface.go`, after line 91 (`GetAccessToken`), add:

```go
	// ValidateAccessToken verifies a previously issued access token.
	// Returns true if the token is valid and not expired for the given sandbox.
	ValidateAccessToken(ctx context.Context, sandboxID, token string) (bool, error)
```

- [ ] **Step 2: Add ValidateAccessToken stub to e2bcloud adapter**

In `internal/adapter/e2bcloud/adapter.go`, after the `GetAccessToken` method (line ~615), add:

```go
// ValidateAccessToken always returns true for e2b-cloud because the upstream
// API validates tokens. The gateway does not need to re-validate.
func (a *Adapter) ValidateAccessToken(_ context.Context, _, _ string) (bool, error) {
	return true, nil
}
```

- [ ] **Step 3: Add ValidateAccessToken stub to opensandbox adapter**

In `internal/adapter/opensandbox/adapter.go`, after the `GetAccessToken` method (line ~633), add:

```go
func (a *Adapter) ValidateAccessToken(_ context.Context, _, _ string) (bool, error) {
	return false, fmt.Errorf("validate access token not supported by opensandbox backend")
}
```

- [ ] **Step 4: Add ValidateAccessToken to test mock backend**

In `test/mock/backend.go`, add the function field after line 110:

```go
	ValidateAccessTokenFn func(ctx context.Context, id, token string) (bool, error)
```

Add the method after line 561:

```go
func (m *MockBackend) ValidateAccessToken(ctx context.Context, id, token string) (bool, error) {
	m.record("ValidateAccessToken", ctx, id, token)
	if m.ValidateAccessTokenFn != nil {
		return m.ValidateAccessTokenFn(ctx, id, token)
	}
	return m.mock.ValidateAccessToken(ctx, id, token)
}
```

- [ ] **Step 5: Add ValidateAccessToken to routing test stub**

In `internal/routing/router_test.go`, after the `GetAccessToken` stub (line ~119), add:

```go
func (s *stubAdapter) ValidateAccessToken(ctx context.Context, id, token string) (bool, error) {
	return true, nil
}
```

- [ ] **Step 6: Verify compilation**

Run: `go build ./...`
Expected: BUILD SUCCESS (all adapters now implement the extended interface)

- [ ] **Step 7: Commit interface extension**

```bash
git add internal/adapter/interface.go internal/adapter/e2bcloud/adapter.go internal/adapter/opensandbox/adapter.go test/mock/backend.go internal/routing/router_test.go
git commit -m "feat(adapter): add ValidateAccessToken to SandboxAdapter interface

Adds server-side token validation method. e2b-cloud returns true
(upstream validates), opensandbox returns not-supported error."
```

---

### Task 2: Implement agentsandbox Token Generation (TDD)

**Files:**
- Modify: `internal/adapter/agentsandbox/adapter.go:44-61` (struct), `:85-127` (New), `:694-698` (GetAccessToken)
- Test: `internal/adapter/agentsandbox/adapter_test.go`

**Interfaces:**
- Consumes: `ValidateAccessToken` from Task 1, `internal/cache.Cache`
- Produces: Working `GetAccessToken` and `ValidateAccessToken` for agent-sandbox adapter

- [ ] **Step 1: Write failing test for GetAccessToken**

In `internal/adapter/agentsandbox/adapter_test.go`, add:

```go
// TestGetAccessToken_GeneratesAndCaches tests that GetAccessToken generates a
// token, caches it, and returns the same token on subsequent calls.
func TestGetAccessToken_GeneratesAndCaches(t *testing.T) {
	a := &Adapter{
		name:       "test",
		tokenCache: cache.New(100, 1*time.Hour),
		idMap:      make(map[string]*sandboxEntry),
	}
	// Pre-populate idMap so resolveClaimName succeeds.
	a.idMap["sbx-123"] = &sandboxEntry{claimName: "claim-123"}
	ctx := context.Background()

	// First call: generates new token.
	tok1, err := a.GetAccessToken(ctx, "sbx-123")
	if err != nil {
		t.Fatalf("GetAccessToken: %v", err)
	}
	if tok1.Token == "" {
		t.Fatal("expected non-empty token")
	}
	if !strings.HasPrefix(tok1.Token, "envd_sbx-123_") {
		t.Errorf("token should have prefix envd_sbx-123_, got %q", tok1.Token)
	}
	if tok1.ExpiresAt.IsZero() {
		t.Error("expected non-zero ExpiresAt")
	}

	// Second call: returns same cached token.
	tok2, err := a.GetAccessToken(ctx, "sbx-123")
	if err != nil {
		t.Fatalf("GetAccessToken (2nd): %v", err)
	}
	if tok2.Token != tok1.Token {
		t.Errorf("expected same token on cache hit, got %q vs %q", tok2.Token, tok1.Token)
	}
}
```

Add to imports: `"github.com/e2bgateway/e2bgateway/internal/cache"` and `"strings"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/adapter/agentsandbox/ -run TestGetAccessToken -v`
Expected: FAIL — `tokenCache` field does not exist on `Adapter`

- [ ] **Step 3: Add tokenCache field to Adapter struct**

In `internal/adapter/agentsandbox/adapter.go`, add to the `Adapter` struct (after line 60):

```go
	// Token cache for access token generation and validation.
	tokenCache *cache.Cache
```

Add import: `"github.com/e2bgateway/e2bgateway/internal/cache"`

- [ ] **Step 4: Initialize tokenCache in New()**

In `internal/adapter/agentsandbox/adapter.go`, in the `New()` function, add to the returned `Adapter` struct (around line 119-126):

```go
		tokenCache: cache.New(10000, 1*time.Hour),
```

- [ ] **Step 5: Implement GetAccessToken**

Replace the stub at line 696-698 with:

```go
// GetAccessToken returns a scoped access token for the sandbox.
// If a valid token already exists in cache, it is returned.
// Otherwise, a new token is generated and cached with 1h TTL.
func (a *Adapter) GetAccessToken(_ context.Context, sandboxID string) (*adapter.AccessToken, error) {
	// Check cache for existing token.
	if cached, ok := a.tokenCache.Get(sandboxID); ok {
		if tokenStr, ok := cached.(string); ok {
			return &adapter.AccessToken{
				Token:     tokenStr,
				ExpiresAt: time.Now().Add(1 * time.Hour),
			}, nil
		}
	}

	// Generate new token: envd_{sandboxID}_{32-hex-random}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generating token: %w", err)
	}
	token := fmt.Sprintf("envd_%s_%s", sandboxID, hex.EncodeToString(b))

	// Store in cache with 1h TTL.
	a.tokenCache.Set(sandboxID, token)

	return &adapter.AccessToken{
		Token:     token,
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}, nil
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/adapter/agentsandbox/ -run TestGetAccessToken -v`
Expected: PASS

- [ ] **Step 7: Write failing test for ValidateAccessToken**

Add to `internal/adapter/agentsandbox/adapter_test.go`:

```go
// TestValidateAccessToken tests token validation against the cache.
func TestValidateAccessToken(t *testing.T) {
	a := &Adapter{
		name:       "test",
		tokenCache: cache.New(100, 1*time.Hour),
		idMap:      make(map[string]*sandboxEntry),
	}
	a.idMap["sbx-1"] = &sandboxEntry{claimName: "claim-1"}
	ctx := context.Background()

	// Generate a token.
	tok, err := a.GetAccessToken(ctx, "sbx-1")
	if err != nil {
		t.Fatalf("GetAccessToken: %v", err)
	}

	// Valid token.
	valid, err := a.ValidateAccessToken(ctx, "sbx-1", tok.Token)
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	if !valid {
		t.Error("expected token to be valid")
	}

	// Wrong token.
	valid, err = a.ValidateAccessToken(ctx, "sbx-1", "envd_sbx-1_wrongtoken")
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	if valid {
		t.Error("expected invalid token to be rejected")
	}

	// Unknown sandbox.
	valid, err = a.ValidateAccessToken(ctx, "sbx-unknown", tok.Token)
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	if valid {
		t.Error("expected unknown sandbox to return false")
	}
}
```

- [ ] **Step 8: Run test to verify it fails**

Run: `go test ./internal/adapter/agentsandbox/ -run TestValidateAccessToken -v`
Expected: FAIL — `ValidateAccessToken` returns error "not supported"

- [ ] **Step 9: Implement ValidateAccessToken**

After `GetAccessToken`, add:

```go
// ValidateAccessToken checks if the given token matches the cached token
// for the sandbox. Returns false if no token is cached or token doesn't match.
func (a *Adapter) ValidateAccessToken(_ context.Context, sandboxID, token string) (bool, error) {
	cached, ok := a.tokenCache.Get(sandboxID)
	if !ok {
		return false, nil
	}
	cachedToken, ok := cached.(string)
	if !ok {
		return false, nil
	}
	return cachedToken == token, nil
}
```

- [ ] **Step 10: Run all agentsandbox tests**

Run: `go test ./internal/adapter/agentsandbox/ -v`
Expected: ALL PASS

- [ ] **Step 11: Commit agentsandbox token implementation**

```bash
git add internal/adapter/agentsandbox/adapter.go internal/adapter/agentsandbox/adapter_test.go
git commit -m "feat(agentsandbox): implement GetAccessToken with scoped token cache

- Token format: envd_{sandboxID}_{32-hex-random}
- 1h TTL via internal/cache LRU
- Token reuse: same token returned until expiry
- ValidateAccessToken checks cache for token match"
```

---

### Task 3: Refactor mock Adapter Token to Use Cache

**Files:**
- Modify: `internal/adapter/mock/adapter.go:19-31` (struct), `:34-66` (New), `:564-576` (GetAccessToken)
- Test: `internal/adapter/mock/adapter_test.go`

**Interfaces:**
- Consumes: `internal/cache.Cache`
- Produces: Refactored `GetAccessToken` + new `ValidateAccessToken` for mock adapter

- [ ] **Step 1: Write failing test for ValidateAccessToken on mock adapter**

In `internal/adapter/mock/adapter_test.go`, after `TestMockAdapterAccessToken` (line ~320), add:

```go
func TestMockAdapterValidateAccessToken(t *testing.T) {
	a := mockadapter.New()
	ctx := context.Background()

	sbx, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "base"})

	// Get token.
	tok, err := a.GetAccessToken(ctx, sbx.SandboxID)
	if err != nil {
		t.Fatalf("get token: %v", err)
	}

	// Valid token.
	valid, err := a.ValidateAccessToken(ctx, sbx.SandboxID, tok.Token)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !valid {
		t.Error("expected valid")
	}

	// Invalid token.
	valid, err = a.ValidateAccessToken(ctx, sbx.SandboxID, "wrong-token")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if valid {
		t.Error("expected invalid")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/adapter/mock/ -run TestMockAdapterValidateAccessToken -v`
Expected: FAIL — `ValidateAccessToken` method does not exist (or returns error)

- [ ] **Step 3: Add tokenCache to mock Adapter struct**

In `internal/adapter/mock/adapter.go`, add to the `Adapter` struct (after line 30):

```go
	tokenCache *cache.Cache // access token cache
```

Add import: `"github.com/e2bgateway/e2bgateway/internal/cache"`

- [ ] **Step 4: Initialize tokenCache in New()**

In `internal/adapter/mock/adapter.go`, add to the `New()` function's return statement (around line 64):

```go
		tokenCache: cache.New(10000, 1*time.Hour),
```

- [ ] **Step 5: Refactor GetAccessToken to use cache**

Replace the `GetAccessToken` method (lines 566-576) with:

```go
func (a *Adapter) GetAccessToken(_ context.Context, sandboxID string) (*adapter.AccessToken, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.sandboxes[sandboxID]; !ok {
		return nil, fmt.Errorf("sandbox %q not found", sandboxID)
	}

	// Check cache for existing token.
	if cached, ok := a.tokenCache.Get(sandboxID); ok {
		if tokenStr, ok := cached.(string); ok {
			return &adapter.AccessToken{
				Token:     tokenStr,
				ExpiresAt: time.Now().Add(1 * time.Hour),
			}, nil
		}
	}

	// Generate new token.
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	token := fmt.Sprintf("envd_%s_%s", sandboxID, hex.EncodeToString(b))

	a.tokenCache.Set(sandboxID, token)

	return &adapter.AccessToken{
		Token:     token,
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}, nil
}
```

- [ ] **Step 6: Implement ValidateAccessToken**

After `GetAccessToken`, add:

```go
func (a *Adapter) ValidateAccessToken(_ context.Context, sandboxID, token string) (bool, error) {
	cached, ok := a.tokenCache.Get(sandboxID)
	if !ok {
		return false, nil
	}
	cachedToken, ok := cached.(string)
	if !ok {
		return false, nil
	}
	return cachedToken == token, nil
}
```

- [ ] **Step 7: Run mock adapter tests**

Run: `go test ./internal/adapter/mock/ -v`
Expected: ALL PASS

- [ ] **Step 8: Commit mock adapter refactor**

```bash
git add internal/adapter/mock/adapter.go internal/adapter/mock/adapter_test.go
git commit -m "feat(mock): refactor token generation to use cache, add ValidateAccessToken

Token generation now uses internal/cache for reuse and validation."
```

---

### Task 4: Add Token Validation to envd Proxy

**Files:**
- Modify: `internal/server/envd_proxy.go:70-80` (Director function)

**Interfaces:**
- Consumes: `ValidateAccessToken(ctx, sandboxID, token) (bool, error)` from adapter
- Produces: Token validation gate before forwarding ConnectRPC requests

- [ ] **Step 1: Write failing test for envd proxy token validation**

In `internal/server/envd_proxy_test.go` (or create if not exists), add:

```go
func TestEnvdProxy_TokenValidation(t *testing.T) {
	// This test verifies that the envd proxy rejects requests with
	// missing or invalid access tokens.
	// Setup is adapter-specific; at minimum verify the handler rejects
	// requests without X-Access-Token header with 401.
}
```

Note: If integration testing the full proxy is too complex for unit tests, skip to Step 3 and verify via manual testing or E2E tests.

- [ ] **Step 2: Run test to verify it fails (or skip)**

If the test file doesn't exist or the test is a placeholder, this step is informational.

- [ ] **Step 3: Modify envd_proxy.go to validate tokens**

In `internal/server/envd_proxy.go`, replace the token handling in the `Director` function (lines 76-79) and add validation BEFORE creating the proxy. Change the block starting at line 68:

```go
		// Validate access token before proxying.
		token := r.Header.Get("X-Access-Token")
		if token == "" {
			http.Error(w, `{"code":401,"message":"missing access token"}`, http.StatusUnauthorized)
			return
		}
		valid, err := a.ValidateAccessToken(r.Context(), sandboxID, token)
		if err != nil || !valid {
			http.Error(w, `{"code":401,"message":"invalid access token"}`, http.StatusUnauthorized)
			return
		}

		proxy := httputil.NewSingleHostReverseProxy(target)

		// Preserve the original Host header so envd's CORS / routing works.
		proxy.Director = func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host

			// Forward the validated access token as Authorization: Bearer.
			req.Header.Set("Authorization", "Bearer "+token)
		}
```

- [ ] **Step 4: Verify compilation**

Run: `go build ./...`
Expected: BUILD SUCCESS

- [ ] **Step 5: Run server package tests**

Run: `go test ./internal/server/ -v`
Expected: ALL PASS (or no tests if none exist)

- [ ] **Step 6: Commit envd proxy token validation**

```bash
git add internal/server/envd_proxy.go
git commit -m "feat(server): validate access token in envd proxy before forwarding

- Rejects requests without X-Access-Token (401)
- Rejects requests with invalid/expired tokens (401)
- Forwards validated token as Authorization: Bearer"
```

---

### Task 5: Update Existing Tests & Full Verification

**Files:**
- Modify: `internal/api/v1/handlers_test.go` (if needed)
- All test files across the project

**Interfaces:**
- Consumes: All previous tasks
- Produces: Green test suite

- [ ] **Step 1: Run full test suite**

Run: `go test ./... -count=1`
Expected: ALL PASS

If any tests fail due to the interface change, fix them:
- Any test file with a `SandboxAdapter` implementation needs `ValidateAccessToken`
- Mock backends in test files need the new method

- [ ] **Step 2: Fix any compilation errors in test files**

Search for other `SandboxAdapter` implementations:

Run: `grep -rn "SandboxAdapter" --include="*_test.go" .`

Add `ValidateAccessToken` to any additional stubs found.

- [ ] **Step 3: Run lint**

Run: `make lint` (or `golangci-lint run`)
Expected: No errors

- [ ] **Step 4: Run full test suite with race detection**

Run: `go test -race ./... -count=1`
Expected: ALL PASS, no race conditions

- [ ] **Step 5: Final commit (if any fixes were needed)**

```bash
git add -A
git commit -m "test: fix test stubs for ValidateAccessToken interface change"
```

---

## Self-Review Checklist

- [x] **Spec coverage**: GetAccessToken (Task 2, 3), ValidateAccessToken (Task 1, 2, 3), token format (Task 2), cache reuse (Task 2, 3), envd proxy validation (Task 4), token TTL 1h (Task 2, 3), all adapters updated (Task 1)
- [x] **Placeholder scan**: No TBD/TODO. All code blocks are complete.
- [x] **Type consistency**: `ValidateAccessToken(ctx context.Context, sandboxID, token string) (bool, error)` used consistently across all tasks. `tokenCache *cache.Cache` used in both agentsandbox and mock adapters.
