# Testing Reference

## Test File Locations

| Layer | Location | Build tag |
|---|---|---|
| Unit | `internal/<pkg>/*_test.go` | (none) |
| Integration | `internal/adapter/<name>/integration_test.go` | (none) |
| E2E | `test/e2e/e2b_api_test.go` | `e2e` |
| Kind E2E | `hack/kind-e2e/` | `e2e` |
| Mock backend | `test/mock/backend.go` | (none) |

## Unit Test Patterns

### Table-driven with subtests

```go
func TestFunctionName(t *testing.T) {
    tests := []struct {
        name    string
        input   InputType
        want    OutputType
        wantErr bool
    }{
        // test cases
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel()  // if safe to parallelize
            got, err := FunctionName(tt.input)
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

### Assertion helpers

```go
require.NoError(t, err)      // fatal on failure
assert.NoError(t, err)       // continues on failure
require.Equal(t, want, got)  // fatal
assert.Equal(t, want, got)   // continues
assert.Regexp(t, pattern, s) // regex match
assert.ElementsMatch(t, want, got)  // unordered slice compare
```

### Mocking with testify

```go
type MockClient struct {
    mock.Mock
}

func (m *MockClient) DoThing(ctx context.Context, arg string) (string, error) {
    args := m.Called(ctx, arg)
    return args.String(0), args.Error(1)
}

func TestXxx(t *testing.T) {
    m := new(MockClient)
    m.On("DoThing", mock.Anything, "hello").
        Return("world", nil).
        Once()

    // ... exercise code ...

    m.AssertExpectations(t)
    m.AssertNumberOfCalls(t, "DoThing", 1)
}
```

## Integration Test Patterns

### Concurrent access

```go
func TestConcurrentAccess(t *testing.T) {
    a := newTestAdapter()
    const N = 50
    var wg sync.WaitGroup
    errs := make([]error, N)

    for i := 0; i < N; i++ {
        wg.Add(1)
        go func(idx int) {
            defer wg.Done()
            _, errs[idx] = a.SomeMethod(context.Background(), "arg")
        }(i)
    }
    wg.Wait()

    for i, err := range errs {
        assert.NoError(t, err, "goroutine %d", i)
    }
}
```

### Context cancellation

```go
func TestContextCancellation(t *testing.T) {
    a := newTestAdapter()
    ctx, cancel := context.WithCancel(context.Background())
    cancel()  // cancel immediately

    _, err := a.SomeMethod(ctx, "arg")
    assert.Error(t, err)
    assert.ErrorIs(t, err, context.Canceled)
}
```

### Shell injection edge cases

```go
func TestShellQuote_EdgeCases(t *testing.T) {
    cases := []string{
        "", "simple", "with space", "with'tick",
        "with;semicolon", "with&amp", "with$var",
        "with`backtick`", "with|pipe", "with\nnewline",
        "$(subshell)", "`command`", "; rm -rf /",
        "unicode: 你好", "with\"double",
    }
    for _, c := range cases {
        t.Run(c, func(t *testing.T) {
            quoted := shellQuote(c)
            // Verify: no unescaped metacharacters outside quotes
            assert.NotContains(t, quoted[1:len(quoted)-1], ";")
            assert.NotContains(t, quoted[1:len(quoted)-1], "&")
            assert.NotContains(t, quoted[1:len(quoted)-1], "$")
        })
    }
}
```

## E2E Test Patterns

### Setup/teardown helpers

```go
func createTestSandbox(t *testing.T) *Sandbox {
    t.Helper()
    resp, err := http.Post(gatewayURL+"/sandboxes", "application/json",
        strings.NewReader(`{"templateID":"base"}`))
    require.NoError(t, err)
    require.Equal(t, http.StatusCreated, resp.StatusCode)

    var sb Sandbox
    require.NoError(t, json.NewDecoder(resp.Body).Decode(&sb))
    t.Cleanup(func() { killSandbox(t, sb.ID) })
    return &sb
}

func killSandbox(t *testing.T, id string) {
    t.Helper()
    req, _ := http.NewRequest("DELETE", gatewayURL+"/sandboxes/"+id, nil)
    resp, err := http.DefaultClient.Do(req)
    require.NoError(t, err)
    assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}
```

### Error format validation

```go
func TestE2E_ErrorFormat(t *testing.T) {
    resp, _ := http.Get(gatewayURL + "/sandboxes/nonexistent")
    assert.Equal(t, http.StatusNotFound, resp.StatusCode)

    var body struct {
        Code    int    `json:"code"`
        Message string `json:"message"`
    }
    require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
    assert.Equal(t, 404, body.Code)
    assert.NotEmpty(t, body.Message)
}
```

## Makefile Targets

```makefile
test:              # go test -race -cover ./...
test-short:        # go test -race -short ./...
test-e2e:          # go test -tags=e2e -race ./test/e2e/...
kind-e2e-setup:    # create Kind cluster
kind-e2e-test:     # run E2E in Kind
kind-e2e-cleanup:  # tear down Kind cluster
coverage:          # generate HTML coverage report
```

## Common Test Failures

| Failure | Cause | Fix |
|---|---|---|
| `race: data race detected` | Concurrent map/slice access without sync | Add `sync.Mutex` or use thread-safe structure |
| `context deadline exceeded` | Test too slow, or real network call | Use shorter timeout, mock network calls |
| `flaky: passes sometimes` | Non-deterministic test (time, goroutine order) | Use `require.Eventually`, fix ordering |
| `import cycle` | Test package imports main package | Use `_test` package suffix for white-box tests |
| `E2E: connection refused` | Gateway not running | Start gateway before E2E, or use test fixtures |

## Coverage Commands

```bash
# Generate coverage profile
go test -coverprofile=coverage.out ./...

# View in browser
go tool cover -html=coverage.out

# Per-package coverage
go test -cover ./internal/adapter/...

# Target: 80%+ for critical paths, 100% for security paths
```
