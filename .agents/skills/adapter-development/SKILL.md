---
name: adapter-development
description: Add or modify a sandbox adapter backend in E2BGateway. Use when implementing a new backend, extending the SandboxAdapter interface, or modifying an existing adapter.
license: Apache-2.0
metadata:
  author: The E2BGateway Authors
---

# Adapter Development Skill for E2BGateway

Use this skill when adding a new sandbox adapter backend or extending the `SandboxAdapter` interface.

## Workflow

### Adding a new backend

1. **Read `AGENTS.md`** → SandboxAdapter Interface section to understand the contract.
2. **Create adapter package**: `internal/adapter/<name>/` with `adapter.go` + `factory.go`.
3. **Implement the full `SandboxAdapter` interface** — every method must have a concrete implementation or a documented "not supported" error.
4. **Implement `factory.go`**: `NewAdapterFromConfig(bcfg config.BackendConfig)` parses the adapter-specific config map.
5. **Register in `internal/server/http.go`** → `initAdapters()`.
6. **Add config schema** in `configs/e2bgateway-example.yaml`.
7. **Add integration tests** in `internal/adapter/<name>/integration_test.go`.
8. **Add E2E test coverage** (at minimum: CRUD, code execution, filesystem, token flow).
9. **Update `AGENTS.md`**:
   - Adapter implementations table
   - Configuration section (if new config fields)
   - Integration test coverage section
10. **Open PR** per `submit-pr` skill.

### Extending the SandboxAdapter interface

1. **Write a design spec first** (use `design-doc` skill) unless it's a trivial addition.
2. **Add the method to `internal/adapter/interface.go`**.
3. **Update ALL four adapters** — this is non-negotiable:

   | Adapter | Package | Notes |
   |---|---|---|
   | agent-sandbox | `internal/adapter/agentsandbox/` | K8s CRD; often needs cache or SDK call |
   | opensandbox | `internal/adapter/opensandbox/` | Alibaba SDK; often dual-mode |
   | e2b-cloud | `internal/adapter/e2bcloud/` | Passthrough; often "upstream validates" |
   | mock | `internal/adapter/mock/` | In-memory; keep it simple |

4. **Update the `routing` package tests** if the interface change affects routing behavior (`internal/routing/router_test.go`).
5. **Update `test/mock/backend.go`** if the mock backend needs to reflect the new method.
6. **Add tests at every layer**:
   - Unit tests in each adapter package
   - Integration tests in primary adapter
   - E2E tests for the new behavior
7. **Update `AGENTS.md`** — interface table, adapter descriptions, config example.

## Interface Checklist

When implementing the full `SandboxAdapter` interface, cover these domains:

| Domain | Methods |
|---|---|
| Lifecycle | `Create`, `List`, `Get`, `Kill`, `Pause`, `Resume`, `SetTimeout` |
| Code Execution | `ExecuteCode`, `ExecuteCodeStream`, `RunCommand` |
| Filesystem | `WriteFile`, `ReadFile`, `UploadFile`, `DownloadFile`, `ListFiles`, `MakeDir`, `RemoveFiles`, `MoveFiles` |
| Templates | `CreateTemplate`, `ListTemplates`, `GetTemplate`, `DeleteTemplate`, tags, aliases, builds |
| Warm Pools | `ListWarmPools`, `CreateWarmPool`, `GetWarmPool`, `UpdateWarmPool`, `DeleteWarmPool` |
| Snapshots | `CreateSnapshot`, `ListSnapshots` |
| Ports | `ListPorts`, `GetPortURL` |
| Access Token | `GetAccessToken`, `ValidateAccessToken` |
| Env Vars | `SetEnvs` |
| Logs | `GetLogs` |
| Data Plane | `GetEnvdEndpoint` — returns envd URL + access token |

## Factory Pattern

Every adapter has a `factory.go`:

```go
package <name>

import (
    "fmt"
    "github.com/e2bgateway/e2bgateway/internal/adapter"
    "github.com/e2bgateway/e2bgateway/internal/config"
)

func NewAdapterFromConfig(bcfg config.BackendConfig) (adapter.SandboxAdapter, error) {
    if bcfg.Config == nil {
        return nil, fmt.Errorf("backend %q: config is required", bcfg.Name)
    }

    // Parse adapter-specific config.
    // CRITICAL: Viper lowercases all map keys. Check BOTH variants:
    //   bcfg.Config["endpoint"] and bcfg.Config["Endpoint"]
    endpoint := getString(bcfg.Config, "endpoint", "Endpoint")
    // ... etc

    return New(
        WithEndpoint(endpoint),
        // ...
    )
}

// getString returns the value for the first matching key (case-insensitive lookup).
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
```

## Viper Case-Sensitivity

**Critical gotcha**: Viper lowercases all map keys when unmarshaling. Adapter factories must check both camelCase and lowercase variants:

```go
// ✅ Correct — handles both "useSignedEndpoint" and "usesignedendpoint"
useSignedEndpoint := getBool(bcfg.Config, "useSignedEndpoint", "usesignedendpoint")

// ❌ Wrong — will miss the lowercase variant
useSignedEndpoint := bcfg.Config["useSignedEndpoint"].(bool)
```

## Access Token Implementation

Every adapter must implement the access token contract:

```go
// GetAccessToken returns a scoped token for the sandbox.
// Token format: envd_{sandboxID}_{32-hex-chars}
// Behavior: check cache first; if present and not expired, return cached token.
// Otherwise, generate new token, cache it (1h TTL), return it.
GetAccessToken(ctx context.Context, sandboxID string) (*adapter.AccessToken, error)

// ValidateAccessToken verifies a token against the cache.
// Returns true if the cached token matches the provided one.
ValidateAccessToken(ctx context.Context, sandboxID, token string) (bool, error)
```

**Per-adapter notes**:

| Adapter | Token generation | Validation |
|---|---|---|
| agent-sandbox | `envd_{id}_{random}`, cached in LRU (10k, 1h) | Cache lookup |
| opensandbox | Dual-mode: `envd_{id}_{random}` OR OSEP-0011 `GetSignedEndpoint` | Cache lookup (random mode) or server validates (signed mode) |
| e2b-cloud | Returns empty / not supported | Returns `true` — upstream E2B validates |
| mock | `envd_{id}_{random}`, cached | Cache lookup |

## envd Data Plane

`GetEnvdEndpoint` returns:
- HTTP URL to the sandbox's `envd` daemon (port 49983)
- Access token the SDK must present

The envd proxy validates the token before forwarding. Implementation varies:

| Adapter | Endpoint resolution | Token behavior |
|---|---|---|
| agent-sandbox | Pod IP:49983 | Returns cached token |
| opensandbox (random mode) | Gateway-constructed URL | Returns cached `envd_{id}_{random}` |
| opensandbox (signed mode) | `GetSignedEndpoint` server call | Returns server-signed token; merges `Endpoint.Headers` |
| e2b-cloud | SDK connects directly via `sandboxDomain` | N/A |

## Common Pitfalls

| Pitfall | How to avoid |
|---|---|
| Forgetting to update all adapters on interface change | Use the checklist above — all 4 adapters must be updated |
| Viper case bug | Always check both camelCase and lowercase keys |
| Shell injection in RunCommand / MakeDir / RemoveFile | Use `shellQuote()` — see `security-coding` skill |
| Heredoc injection in WriteFile | Use `UploadFile` with `bytes.NewReader` — never shell heredoc |
| TOCTOU race in lazy init | Use `sync.Once` or double-check locking with cleanup on Kill |
| Binary data corruption | Never `string(data)` for binary content; use `bytes.NewReader` |
| Missing E2E test | Add E2E tests for every new adapter method |
| `AGENTS.md` drift | Always include "update AGENTS.md" in the PR checklist |

## Examples in This Repo

- Adding access token to all adapters: `#42` (interface + agentsandbox), `#43` (opensandbox), `#44` (opensandbox hybrid mode)
- Security fixes across adapters: `#15` (comprehensive adapter improvements)
- Adding OpenSandbox backend: see git history for `internal/adapter/opensandbox/`

See [references/REFERENCE.md](references/REFERENCE.md) for a complete adapter implementation skeleton.
