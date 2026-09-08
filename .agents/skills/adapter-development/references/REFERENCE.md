# Adapter Development Reference

## Adapter Skeleton

```go
// internal/adapter/<name>/adapter.go

package <name>

import (
    "context"
    "fmt"
    "sync"

    "github.com/e2bgateway/e2bgateway/internal/adapter"
    "github.com/e2bgateway/e2bgateway/internal/cache"
)

type Adapter struct {
    name       string
    // ... backend-specific fields

    // tokenCache for GetAccessToken/ValidateAccessToken
    tokenCache *cache.Cache
    mu         sync.Mutex
}

var _ adapter.SandboxAdapter = (*Adapter)(nil) // compile-time check

func New(opts ...Option) (*Adapter, error) {
    a := &Adapter{
        tokenCache: cache.New(10000, 1*time.Hour),
    }
    for _, o := range opts {
        o(a)
    }
    return a, nil
}

// --- Lifecycle ---

func (a *Adapter) Create(ctx context.Context, req *adapter.CreateSandboxRequest) (*adapter.Sandbox, error) {
    // TODO: call backend SDK
    return nil, fmt.Errorf("not implemented")
}

// ... implement every method in the interface

// --- Access Token ---

func (a *Adapter) GetAccessToken(_ context.Context, sandboxID string) (*adapter.AccessToken, error) {
    if cached, ok := a.tokenCache.Get(sandboxID); ok {
        return &adapter.AccessToken{Token: cached.(string)}, nil
    }

    token := fmt.Sprintf("envd_%s_%s", sandboxID, randomHex(16))
    a.tokenCache.Set(sandboxID, token)
    return &adapter.AccessToken{Token: token}, nil
}

func (a *Adapter) ValidateAccessToken(_ context.Context, sandboxID, token string) (bool, error) {
    cached, ok := a.tokenCache.Get(sandboxID)
    if !ok {
        return false, nil
    }
    return cached.(string) == token, nil
}

// --- Data Plane ---

func (a *Adapter) GetEnvdEndpoint(ctx context.Context, sandboxID string) (string, string, error) {
    // Returns (endpointURL, accessToken, error)
    token, err := a.GetAccessToken(ctx, sandboxID)
    if err != nil {
        return "", "", err
    }
    endpoint := fmt.Sprintf("http://<pod-ip>:49983")
    return endpoint, token.Token, nil
}
```

## Factory Skeleton

```go
// internal/adapter/<name>/factory.go

package <name>

import (
    "fmt"

    "github.com/e2bgateway/e2bgateway/internal/adapter"
    "github.com/e2bgateway/e2bgateway/internal/config"
)

func NewAdapterFromConfig(bcfg config.BackendConfig) (adapter.SandboxAdapter, error) {
    if bcfg.Config == nil {
        return nil, fmt.Errorf("backend %q: config map is required", bcfg.Name)
    }

    // Viper lowercases all keys — check both variants.
    endpoint := getString(bcfg.Config, "endpoint", "Endpoint")
    if endpoint == "" {
        return nil, fmt.Errorf("backend %q: endpoint is required", bcfg.Name)
    }
    timeout := getDuration(bcfg.Config, "timeout", "Timeout", 60*time.Second)
    // ... other fields

    return New(
        WithEndpoint(endpoint),
        WithTimeout(timeout),
    )
}

func getString(m map[string]any, keys ...string) string {
    for _, k := range keys {
        if v, ok := m[k]; ok {
            if s, ok := v.(string); ok {
                return s
            }
        }
    }
    return ""
}

func getDuration(m map[string]any, keys ...string) time.Duration {
    // ... similar pattern, handle string and time.Duration types
}

func getBool(m map[string]any, keys ...string) bool {
    for _, k := range keys {
        if v, ok := m[k]; ok {
            if b, ok := v.(bool); ok {
                return b
            }
        }
    }
    return false
}
```

## Registration in `internal/server/http.go`

```go
func initAdapters(cfg *config.Config) map[string]adapter.SandboxAdapter {
    registry := make(map[string]adapter.SandboxAdapter)

    for _, bcfg := range cfg.Backends {
        if !bcfg.Enabled {
            continue
        }

        var a adapter.SandboxAdapter
        var err error

        switch bcfg.Type {
        case "agent-sandbox":
            a, err = agentsandbox.NewAdapterFromConfig(bcfg)
        case "opensandbox":
            a, err = opensandbox.NewAdapterFromConfig(bcfg)
        case "e2b-cloud":
            a, err = e2bcloud.NewAdapterFromConfig(bcfg)
        case "mock":
            a, err = mock.NewAdapterFromConfig(bcfg)
        case "<name>":  // <-- add your new adapter here
            a, err = <name>.NewAdapterFromConfig(bcfg)
        default:
            slog.Error("unknown backend type", "type", bcfg.Type, "name", bcfg.Name)
            continue
        }

        if err != nil {
            slog.Error("failed to init adapter", "name", bcfg.Name, "error", err)
            continue
        }

        registry[bcfg.Name] = a
    }

    return registry
}
```

## Config Example Entry

```yaml
# configs/e2bgateway-example.yaml

backends:
  - name: my-<name>
    type: <name>
    enabled: true
    config:
      endpoint: "http://<runtime>:8080"
      timeout: 60s
      pool:
        maxIdleConns: 50
        maxIdleConnsPerHost: 25
      # Adapter-specific options:
      # optionName: value
```

## Testing Checklist

### Unit tests (`adapter_test.go`)
- Every public method has at least one positive + one negative test
- Table-driven tests for edge cases (shell injection, PID validation)
- Use `testify/assert` and `testify/require`

### Integration tests (`integration_test.go`)
- Real backend or real SDK client
- Cover: create, execute, filesystem, kill, token flow
- Verify security: shell injection, PID validation, heredoc injection
- Concurrent access patterns
- Context cancellation

### E2E tests (`test/e2e/`)
- CRUD: `TestE2E_CreateSandbox`, `TestE2E_ListSandboxes`, `TestE2E_GetSandbox`, `TestE2E_KillSandbox`
- Code execution: `TestE2E_ExecuteCode`
- Filesystem: `TestE2E_WriteFile`, `TestE2E_ReadFile`, `TestE2E_UploadFile`, `TestE2E_DownloadFile`
- Access token: `TestE2E_AccessToken_Format`, `TestE2E_AccessToken_Reuse`, `TestE2E_EnvdProxy_MissingToken`, `TestE2E_EnvdProxy_InvalidToken`, `TestE2E_EnvdProxy_ValidToken`
