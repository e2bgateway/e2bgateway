---
name: testing
description: Write tests for E2BGateway. Use when writing unit tests, integration tests, or E2E tests — covers conventions, layer selection, mocks, and race detection.
license: Apache-2.0
metadata:
  author: The E2BGateway Authors
---

# Testing Skill for E2BGateway

Use this skill when writing or reviewing tests for E2BGateway.

## Workflow

1. **Identify the test layer** — unit, integration, or E2E. See "Test Layers" below.
2. **Pick the right file location**:
   - Unit: `internal/<package>/<file>_test.go`
   - Integration: `internal/adapter/<name>/integration_test.go`
   - E2E: `test/e2e/e2b_api_test.go`
3. **Use table-driven tests** for multiple scenarios.
4. **Use `testify`** (`assert`, `require`, `mock`) for assertions.
5. **Always run with `-race`** — data races are bugs.
6. **Write security tests** — validate all user-input sanitization.
7. **Run locally before pushing**:
   ```bash
   make test              # unit tests with -race
   make pre-commit        # full pre-commit suite
   ```

## Test Layers

| Layer | Location | Scope | Speed | Requires backend? |
|---|---|---|---|---|
| **Unit** | `internal/<pkg>/*_test.go` | Single function/method | Fast | No (mocks) |
| **Integration** | `internal/adapter/<name>/integration_test.go` | Adapter end-to-end | Medium | Sometimes |
| **E2E** | `test/e2e/e2b_api_test.go` | Full HTTP API compliance | Slow | Yes |
| **Kind E2E** | `hack/kind-e2e/` | Full integration in K8s | Slowest | Yes (Kind) |

### When to use each

- **Unit test**: Logic that doesn't need external dependencies. Parsing, validation, cache behavior, config parsing.
- **Integration test**: Adapter methods that benefit from real SDK interaction. Token caching, concurrent access, shell quote edge cases.
- **E2E test**: HTTP API behavior the SDK depends on. CRUD endpoints, error format, response shape, token flow in envd proxy.
- **Kind E2E**: Full deployment validation. Helm install + real backends + multi-language SDK examples.

## Test Structure

### Table-driven tests (preferred)

```go
func TestSomething(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        want    string
        wantErr bool
    }{
        {"normal case", "input", "output", false},
        {"edge case", "", "", true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := doSomething(tt.input)
            if tt.wantErr {
                require.Error(t, err)
                return
            }
            require.NoError(t, err)
            assert.Equal(t, tt.want, got)
        })
    }
}
```

### Test naming

- `Test<FunctionName>_<Scenario>` for specific scenarios
- `Test<FunctionName>` for the happy path
- `Test<Feature>_<Layer>` for integration/E2E (e.g., `TestAccessToken_GetEnvdEndpoint_Integration`)

## Layer-Specific Guidance

### Unit Tests

**Location**: Same package as the code under test (white-box).

**Patterns**:
- Mock external dependencies (SDK clients, caches, HTTP clients)
- Test error paths as well as happy paths
- Verify security rules: shell injection, PID validation, etc.

```go
// Example: Token cache behavior
func TestGetAccessToken_GeneratesAndCaches(t *testing.T) {
    a := newTestAdapter()

    // First call: generates new token
    tok1, err := a.GetAccessToken(context.Background(), "sandbox-1")
    require.NoError(t, err)
    assert.Regexp(t, `^envd_sandbox-1_[0-9a-f]{32}$`, tok1.Token)

    // Second call: returns cached token
    tok2, err := a.GetAccessToken(context.Background(), "sandbox-1")
    require.NoError(t, err)
    assert.Equal(t, tok1.Token, tok2.Token)
}
```

### Integration Tests

**Location**: `internal/adapter/<name>/integration_test.go`

**Patterns**:
- Test real SDK behavior where possible
- Cover: shell quoting edge cases, PID parsing, concurrent access, context cancellation
- Verify security fixes (see `security-coding` skill)

```go
// Example: Concurrent token access
func TestGetAccessToken_Concurrent(t *testing.T) {
    a := newTestAdapter()
    const N = 100
    var wg sync.WaitGroup
    tokens := make([]string, N)

    for i := 0; i < N; i++ {
        wg.Add(1)
        go func(idx int) {
            defer wg.Done()
            tok, err := a.GetAccessToken(context.Background(), "sandbox-1")
            require.NoError(t, err)
            tokens[idx] = tok.Token
        }(i)
    }
    wg.Wait()

    // All tokens should be identical (cache hit after first)
    for i := 1; i < N; i++ {
        assert.Equal(t, tokens[0], tokens[i])
    }
}
```

### E2E Tests

**Location**: `test/e2e/e2b_api_test.go`

**Run with**: `make test-e2e` (requires `-tags=e2e`).

**Patterns**:
- Test HTTP API compliance with E2B protocol
- Verify response format: `{"code": int, "message": string}` for errors
- Cover all CRUD endpoints, code execution, filesystem, tokens

```go
// Example: Token flow E2E
func TestE2E_AccessToken_Format(t *testing.T) {
    sandbox := createTestSandbox(t)
    defer killSandbox(t, sandbox.ID)

    resp := requestAccessToken(t, sandbox.ID)
    require.Equal(t, http.StatusOK, resp.StatusCode)

    var body struct {
        Token string `json:"token"`
    }
    require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
    assert.Regexp(t, `^envd_.+_[0-9a-f]{32}$`, body.Token)
}

func TestE2E_EnvdProxy_MissingToken(t *testing.T) {
    sandbox := createTestSandbox(t)
    defer killSandbox(t, sandbox.ID)

    req, _ := http.NewRequest("POST", envdURL(sandbox.ID)+"/commands", nil)
    resp := httpClient().Do(req)
    assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
```

## Required Test Coverage

### For a new adapter method

- [ ] Unit test: happy path + error path + edge cases
- [ ] Integration test in primary adapter (agentsandbox or opensandbox)
- [ ] E2E test if it's exposed via HTTP API
- [ ] Security test if it touches user input

### For an interface change

- [ ] All 4 adapters implement the method
- [ ] Unit tests in mock adapter at minimum
- [ ] Integration tests in primary adapter
- [ ] E2E tests if exposed

### For a security fix

- [ ] Negative test for the attack vector (must reject malicious input)
- [ ] Positive tests for edge cases (must not break legitimate use)
- [ ] Regression test that would have caught the original bug

## Race Detection

**Always** run tests with `-race`:

```bash
make test              # includes -race
go test -race ./...    # same
```

**Common race patterns to watch for**:

| Pattern | Fix |
|---|---|
| Concurrent cache access | Use `sync.Mutex` or thread-safe cache (like `internal/cache`) |
| Lazy-init double-check | Use `sync.Once` or proper locking |
| Map read/write | Use `sync.RWMutex` |
| Slice append in goroutines | Pre-allocate + index access, or use channels |

## Mocks

Use `testify/mock` for mocking dependencies:

```go
type MockSDKClient struct {
    mock.Mock
}

func (m *MockSDKClient) CreateSandbox(ctx context.Context, req *api.CreateRequest) (*api.Sandbox, error) {
    args := m.Called(ctx, req)
    return args.Get(0).(*api.Sandbox), args.Error(1)
}

func TestCreate_UsesSDK(t *testing.T) {
    sdk := new(MockSDKClient)
    sdk.On("CreateSandbox", mock.Anything, mock.Anything).
        Return(&api.Sandbox{ID: "sb-1"}, nil)

    a := New(WithSDK(sdk))
    got, err := a.Create(context.Background(), &adapter.CreateSandboxRequest{})

    require.NoError(t, err)
    assert.Equal(t, "sb-1", got.ID)
    sdk.AssertExpectations(t)
}
```

## Running Tests

```bash
make test              # all unit tests with -race
make test-short        # skip slow tests
make test-e2e          # E2E tests (requires running gateway + backend)
make kind-e2e-test     # full E2E in Kind cluster
go test -v ./internal/adapter/agentsandbox/ -run TestShellQuote
go test -coverprofile=coverage.out ./... && go tool cover -html=coverage.out
```

## Coverage Target

- **80%+** for critical paths (adapters, envd proxy, auth, routing)
- **100%** for security-critical paths (shell quoting, PID validation, token validation)

See [references/REFERENCE.md](references/REFERENCE.md) for testing patterns and examples.
