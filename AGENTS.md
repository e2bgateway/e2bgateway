# AGENTS.md — E2BGateway Developer Guide

## Overview

E2BGateway is a **stateless, horizontally-scalable API gateway** that provides full compatibility with the E2B Client SDK protocol (HTTP + WebSocket + ConnectRPC). It routes requests to diverse sandbox runtimes — allowing organizations to use standard E2B Python/JS SDKs while running sandboxes on their own infrastructure.

- **Language**: Go 1.26+
- **Module**: `github.com/e2bgateway/e2bgateway`
- **Entry point**: `cmd/e2bgateway/main.go` → `internal/cmd/root.go`
- **Requires**: At least one sandbox backend (agent-sandbox, opensandbox, e2b-cloud, or mock)

---

## Build & Development Commands

### Core

```bash
make build              # Cross-compile for linux/amd64
make build-local        # Build for current platform
make run                # Build + run locally
make test               # Unit tests with -race
make test-short         # Short-mode tests (skip slow)
make test-e2e           # E2E tests (requires -tags=e2e)
make lint               # golangci-lint
make fmt                # gofumpt formatting
make vet                # go vet
make pre-commit         # fmt + vet + lint + test
```

### Docker

```bash
make docker-build       # Build gateway container image
make docker-push        # Push to registry
docker build -t sandbox-with-envd:local images/sandbox/   # Build envd-embedded sandbox image
```

### Local Kind E2E

```bash
make kind-e2e-setup     # Create kind cluster
make kind-e2e-test      # Run full E2E in kind
make kind-e2e-cleanup   # Tear down kind cluster
```

### Helm

```bash
make helm-lint          # Validate Helm chart
make helm-template      # Render Helm templates
```

---

## Architecture

### Request Flow

```
E2B SDK (Python/JS/Go/cURL)
       │
       ▼
┌──────────────────────────────────────────────┐
│  E2BGateway (chi router)                     │
│                                              │
│  Middleware chain:                           │
│    RequestID → RealIP → Logger → Recovery   │
│    → CORS → Auth → RateLimit → AuditLog     │
│                                              │
│  Routes:                                     │
│    /sandboxes/*          (root)              │
│    /v2/sandboxes/*       (v2 prefix)         │
│    /api/v1/sandboxes/*   (legacy prefix)     │
│    /templates/*                              │
│    /healthz, /readyz, /metrics               │
│    /* (catch-all → envd proxy)              │
└───────────┬──────────────────────────────────┘
            │
    ┌───────┴───────┐
    ▼               ▼
 Control        Data Plane
 Plane          (envd reverse proxy)
 (REST API)          │
    │               ▼
    ▼          httputil.ReverseProxy
 SandboxAdapter      │
    │                ▼
    ▼           ┌─────────────┐
 Backend        │  Sandbox     │
 Runtime        │  Container   │
                │  (envd:49983)│
                └─────────────┘
```

**Control plane** (REST API): Sandbox CRUD, templates, warm pools — handled by `SandboxAdapter` implementations.

**Data plane** (ConnectRPC): Commands, filesystem, code execution — reverse-proxied to `envd` daemon running inside each sandbox container on port 49983.

### SandboxAdapter Interface

Defined in `internal/adapter/interface.go`. The core abstraction — all sandbox runtimes implement this:

| Domain | Methods |
|---|---|
| **Lifecycle** | `Create`, `List`, `Get`, `Kill`, `Pause`, `Resume`, `SetTimeout` |
| **Code Execution** | `ExecuteCode`, `ExecuteCodeStream`, `RunCommand` |
| **Filesystem** | `WriteFile`, `ReadFile`, `UploadFile`, `DownloadFile`, `ListFiles`, `MakeDir`, `RemoveFiles`, `MoveFiles` |
| **Templates** | `CreateTemplate`, `ListTemplates`, `GetTemplate`, `DeleteTemplate`, aliases, tags, builds |
| **Warm Pools** | `ListWarmPools`, `CreateWarmPool`, `GetWarmPool`, `UpdateWarmPool`, `DeleteWarmPool` |
| **Data Plane** | `GetEnvdEndpoint` — returns HTTP URL for the sandbox's envd daemon |

### Adapter Implementations

| Adapter | Package | Description |
|---|---|---|
| **agent-sandbox** | `internal/adapter/agentsandbox/` | K8s CRD via `sigs.k8s.io/agent-sandbox` (SandboxClaim). Resolves envd endpoint via Pod IP. |
| **opensandbox** | `internal/adapter/opensandbox/` | Alibaba OpenSandbox SDK. Template→image mapping. Per-sandbox ExecdClient cache. |
| **e2b-cloud** | `internal/adapter/e2bcloud/` | Passthrough proxy to real E2B Cloud API. SDK connects to envd directly via `sandboxDomain`. |
| **mock** | `internal/adapter/mock/` | In-memory implementation for testing. Pre-populated with "base" and "code-interpreter" templates. |

Each adapter has a `factory.go` with `NewAdapterFromConfig(bcfg config.BackendConfig)` that parses the backend-specific config map.

### Adding a New Backend

1. Create `internal/adapter/<name>/` with `adapter.go` + `factory.go`
2. Implement the full `SandboxAdapter` interface
3. Register in `internal/server/http.go` → `initAdapters()`
4. Add config schema in `configs/e2bgateway-example.yaml`

### envd Data Plane Proxy

`internal/server/envd_proxy.go` — catch-all reverse proxy for ConnectRPC:

1. Extracts sandbox ID from `E2b-Sandbox-Id` header or Host header (`{port}-{sandboxID}.{domain}`)
2. Routes to correct adapter via `routing.Router`
3. Calls `adapter.GetEnvdEndpoint()` to get envd URL
4. Forwards request via `httputil.ReverseProxy` (supports HTTP streaming for server-stream RPCs)
5. Sets `Authorization: Bearer <token>` from `X-Access-Token` header

### Routing

`internal/routing/router.go` — backend selection with health checking:

**Strategy order**: template-based → weighted round-robin → priority/failover chain → default → first healthy → any

Background goroutine pings backends; unhealthy backends are skipped after threshold consecutive failures.

### Authentication

`internal/auth/auth.go` — `Manager` orchestrates multiple `Provider` instances:

- **APIKeyProvider**: Static keys from config, configurable header name
- **JWTProvider**: Token validation
- **RateLimiter**: Token-bucket per key (tenant or IP), configurable RPM

---

## Key Packages

| Package | Responsibility |
|---|---|
| `cmd/e2bgateway/` | Thin entry point |
| `internal/cmd/` | Cobra CLI: `root.go` (flags), `serve.go` (startup), `version.go` |
| `internal/config/` | Viper-based config loading with `E2BGW_` env prefix, validation |
| `internal/server/` | chi router, route registration, middleware chain, envd proxy |
| `internal/server/middleware/` | RealIP, RequestLogger, Recovery, CORS, Auth, RateLimit, AuditLog |
| `internal/api/v1/` | ~40 HTTP handlers for all E2B API endpoints |
| `internal/api/dto/` | E2B wire-format DTOs (JSON request/response types) |
| `internal/adapter/` | SandboxAdapter interface + all backend implementations |
| `internal/auth/` | Auth manager, providers, rate limiter, tenant context |
| `internal/routing/` | Backend selection, health checking, failover |
| `internal/streaming/` | WebSocket handler, frame relay, normalizer, backpressure buffer |
| `internal/cache/` | Thread-safe LRU cache with TTL |
| `internal/observability/` | OpenTelemetry init (traces + metrics via OTLP gRPC) |
| `pkg/api/` | Public API types and constants for external consumers |
| `pkg/client/` | Go SDK client for gateway admin API |
| `configs/` | Default and example YAML configurations |
| `deploy/` | Dockerfile, Helm chart, Kustomize overlays (dev/staging/prod) |
| `images/sandbox/` | Dockerfile + entrypoint for envd-embedded sandbox base image |
| `examples/` | Go, Python, JavaScript, cURL examples demonstrating SDK usage |
| `test/e2e/` | ~30 E2E tests for E2B format compliance |
| `hack/kind-e2e/` | Scripts for local Kind cluster E2E validation |

---

## Configuration

YAML config loaded by Viper with `E2BGW_` environment variable prefix. See `configs/e2bgateway-example.yaml` for full reference.

**Top-level structure**:

```yaml
server:
  httpPort: 8080
  httpsPort: 8443
  metricsPort: 9090
  envdDomain: "e2b.example.com"     # Domain for SDK envd URL construction

backends:
  - name: my-sandbox
    type: agent-sandbox               # agent-sandbox | opensandbox | e2b-cloud | mock
    enabled: true
    config: { ... }                   # Adapter-specific settings

auth:
  providers:
    - type: apikey
      keys: ["test-key"]

rateLimit:
  enabled: true
  defaultRPM: 60

routing:
  strategy: static                    # static | template-based | weighted-round-robin | priority-chain
  defaultBackend: my-sandbox

observability:
  enabled: true
  otlp:
    endpoint: "otel-collector:4317"
```

**Important**: Viper lowercases all map keys, so adapter factories check both camelCase and lowercase variants when parsing `config` maps.

---

## Testing

E2BGateway uses a comprehensive multi-layer testing strategy:

### Test Layers

1. **Unit Tests** (`*_test.go` in each package)
   - Test individual functions and methods
   - Use mocks for external dependencies
   - Fast execution, high coverage
   ```bash
   make test              # All unit tests with race detection
   make test-short        # Skip slow tests
   ```

2. **Integration Tests** (`internal/adapter/*/integration_test.go`)
   - Test adapter implementations end-to-end
   - Verify security fixes (shell injection, PID validation, etc.)
   - Test concurrent access and race conditions
   - Validate error handling and edge cases
   ```bash
   go test ./internal/adapter/agentsandbox/ -v
   go test ./internal/adapter/opensandbox/ -v
   ```

3. **E2E Tests** (`test/e2e/`)
   - Test full HTTP API compliance with E2B protocol
   - Require running gateway + backend
   ```bash
   make test-e2e          # Requires -tags=e2e
   ```

4. **Kind E2E Tests** (`hack/kind-e2e/`)
   - Full integration in Kubernetes environment
   - Tests all backends (agent-sandbox, opensandbox)
   - Validates Helm deployment
   ```bash
   make kind-e2e-setup && make kind-e2e-test
   ```

### Integration Test Coverage

**Agent-Sandbox** (`internal/adapter/agentsandbox/integration_test.go`):
- Shell quote escaping (12 edge cases)
- Process listing and PID parsing
- PID validation (prevents shell injection)
- Environment variable persistence
- Command construction (MakeDir, RemoveFile, RunCommand)
- Binary data handling
- Concurrent access patterns
- Context cancellation

**OpenSandbox** (`internal/adapter/opensandbox/integration_test.go`):
- Process management with real PID parsing
- PID validation and injection prevention
- WriteFile security (no heredoc)
- ExecdClient cache concurrent access
- Binary data preservation
- Timeout and context handling

### Test Best Practices

1. **Table-driven tests** for multiple scenarios
2. **Use `testify`** for assertions (`assert`, `require`, `mock`)
3. **Race detection**: Always run with `-race` flag
4. **Coverage target**: 80%+ for critical paths
5. **Security tests**: Validate all user input sanitization

```go
// Example: Security test for PID validation
func TestKillProcess_Validation(t *testing.T) {
    tests := []struct {
        name      string
        processID string
        wantErr   bool
    }{
        {"valid PID", "1234", false},
        {"shell injection", "123; rm -rf /", true},
        {"empty", "", true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            var pid int
            var extra string
            n, err := fmt.Sscanf(tt.processID, "%d%s", &pid, &extra)
            shouldFail := n != 1 || (err != nil && err != io.EOF)
            assert.Equal(t, tt.wantErr, shouldFail)
        })
    }
}
```

### Coverage Reports

```bash
make coverage          # Generate HTML coverage report
# Opens coverage.html in browser
```

**E2E test coverage** (`test/e2e/e2b_api_test.go`):
- Sandbox CRUD, response format validation
- Error format (`{"code": int, "message": string}`)
- Code execution, commands, filesystem operations
- Templates (CRUD, tags, aliases, builds)
- V2 + legacy `/api/v1` routes
- Warm pools, snapshots, ports, processes
- envd-compatible paths

**CI E2E** (`.github/workflows/e2e.yml`): Runs Go, Python, JavaScript, and cURL examples against both agent-sandbox and opensandbox backends in Kind.

---

## Coding Conventions

### Security Practices

**CRITICAL**: All user input must be sanitized to prevent shell injection and other security issues.

1. **Shell Command Construction**
   - **NEVER** concatenate user input directly into shell commands
   - **ALWAYS** use proper escaping via `shellQuote()` function
   ```go
   // ❌ WRONG - Shell injection vulnerability
   cmd := "mkdir -p " + userInput
   
   // ✅ CORRECT - Safe shell escaping
   cmd := "mkdir -p " + shellQuote(userInput)
   ```

2. **PID Validation**
   - Validate PIDs are pure numbers before using in commands
   - Use strict parsing to prevent injection via process IDs
   ```go
   var pid int
   var extra string
   n, err := fmt.Sscanf(processID, "%d%s", &pid, &extra)
   if n != 1 || (err != nil && err != io.EOF) {
       return fmt.Errorf("invalid process ID: must be numeric")
   }
   ```

3. **File Operations**
   - **NEVER** use heredoc for file writing (vulnerable to content injection)
   - Use binary-safe methods: `bytes.NewReader()` for uploads
   ```go
   // ❌ WRONG - Heredoc injection if content contains "EOF"
   cmd := fmt.Sprintf("cat > %s << 'EOF'\n%s\nEOF", path, content)
   
   // ✅ CORRECT - Binary-safe upload
   return a.UploadFile(ctx, sandboxID, &adapter.FileUploadRequest{
       Path:   path,
       Reader: io.NopCloser(bytes.NewReader(content)),
   })
   ```

4. **Binary Data Handling**
   - Never cast `[]byte` to `string` for binary data
   - Use `bytes.NewReader()` to preserve binary content
   ```go
   // ❌ WRONG - Corrupts binary data
   return io.NopCloser(strings.NewReader(string(data)))
   
   // ✅ CORRECT - Preserves binary data
   return io.NopCloser(bytes.NewReader(data))
   ```

5. **Environment Variables**
   - Write to persistent files (`/etc/environment`) for cross-shell persistence
   - Use proper quoting: `KEY="value"` format
   ```go
   envLines := make([]string, 0, len(envs))
   for k, v := range envs {
       envLines = append(envLines, fmt.Sprintf("%s=%s", k, shellQuote(v)))
   }
   content := strings.Join(envLines, "\n") + "\n"
   cmd := fmt.Sprintf("echo %s >> /etc/environment", shellQuote(content))
   ```

### Recent Security Fixes (P0)

The following security vulnerabilities were identified and fixed:

1. **Shell Injection** (`internal/adapter/agentsandbox/adapter.go`)
   - Added `shellQuote()` helper function
   - Fixed `MakeDir`, `RemoveFile`, `RunCommand` to escape all user inputs
   - Integration tests verify 12+ edge cases

2. **Heredoc Injection** (`internal/adapter/opensandbox/adapter.go`)
   - Replaced heredoc-based `WriteFile` with `UploadFile`
   - Binary-safe and immune to content injection

3. **TOCTOU Race Condition** (`internal/adapter/opensandbox/adapter.go`)
   - Fixed `getOrCreateExecdClient` double-check locking pattern
   - Added cleanup in `KillSandbox` to prevent memory leaks

4. **PID Validation** (both adapters)
   - Strict numeric validation prevents shell injection via process IDs
   - Integration tests verify rejection of malicious inputs

### Other Conventions

- **Go 1.26+** with standard library + minimal dependencies
- **Error format**: All API errors return `{"code": int, "message": string}` — see `dto.ErrorResponse`
- **Interface-first**: New backends implement `SandboxAdapter`; never call backend-specific APIs from handlers
- **Handler pattern**: Extract sandboxID from `chi.URLParam` → iterate `registry.List()` trying each adapter → first success wins
- **DTOs in `internal/api/dto/`**: All E2B wire-format types live here; domain types in `internal/adapter/interface.go`
- **Concurrency**: Use `-race` flag in all tests; adapter implementations must be goroutine-safe
- **Config parsing**: Adapter `factory.go` must handle Viper's lowercased keys (check both `camelCase` and `lowercase`)
- **Streaming**: Use `httputil.ReverseProxy` for envd proxy (supports HTTP/2 streaming); do not buffer ConnectRPC responses
- **Logging**: Structured logging via `slog`; include `sandboxID`, `adapter`, `requestID` in context
- **Context propagation**: Always pass `context.Context` as first argument; respect cancellation in all adapter methods
- **Generated code**: Files marked `// Code generated` should not be edited manually
