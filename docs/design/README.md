# E2BGateway Architecture Design

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Problem Statement](#2-problem-statement)
3. [Design Goals & Non-Goals](#3-design-goals--non-goals)
4. [System Architecture](#4-system-architecture)
5. [API Surface](#5-api-surface)
6. [Backend Adapter Framework](#6-backend-adapter-framework)
7. [Control Plane vs Data Plane](#7-control-plane-vs-data-plane)
8. [Streaming & WebSocket](#8-streaming--websocket)
9. [Authentication & Multi-Tenancy](#9-authentication--multi-tenancy)
10. [Routing & Load Balancing](#10-routing--load-balancing)
11. [Observability](#11-observability)
12. [Deployment Models](#12-deployment-models)
13. [Configuration](#13-configuration)
14. [Security Model](#14-security-model)
15. [Performance & Scalability](#15-performance--scalability)
16. [Migration Strategy](#16-migration-strategy)
17. [Project Structure](#17-project-structure)
18. [Implementation Phases](#18-implementation-phases)

---

## 1. Executive Summary

E2BGateway is a **stateless, horizontally-scalable API gateway** that provides full compatibility with the E2B Client protocol (HTTP + WebSocket), transparently routing requests to diverse underlying agent sandbox runtimes. It acts as a **universal abstraction layer** that eliminates vendor lock-in and runtime fragmentation in the AI agent sandbox ecosystem.

```
                    ┌──────────────────────────────────────┐
                    │     E2B SDK (Python / JS / Go)       │
                    │     E2B CLI  /  AI Agent Frameworks  │
                    └──────────────┬───────────────────────┘
                                   │ HTTPS / WSS
                                   │ (E2B Protocol)
                                   ▼
┌──────────────────────────────────────────────────────────────────────────┐
│                               E2B  GATEWAY                               │
│                                                                          │
│  ┌─────────────┐  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐   │
│  │   AuthN/Z   │  │  Rate Limit  │  │   Router     │  │  Audit Log   │   │
│  │  Middleware │  │  Middleware  │  │  Middleware  │  │  Middleware  │   │
│  └──────┬──────┘  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘   │
│         └────────────────┴─────────────────┴─────────────────┘           │
│                                   │                                      │
│                    ┌──────────────┴──────────────┐                       │
│                    │      Protocol Translator    │                       │
│                    │  (E2B API ↔ Backend Proto)  │                       │
│                    └──────────────┬──────────────┘                       │
│                                   │                                      │
│              ┌────────────────────┼────────────────────┐                 │
│              ▼                    ▼                    ▼                 │
│  ┌───────────────────┐ ┌─────────────────┐ ┌───────────────────┐         │
│  │  E2B Cloud        │ │ agent-sandbox   │ │  OpenSandbox      │         │
│  │  Adapter          │ │ Adapter         │ │  Adapter          │         │
│  │  (passthrough)    │ │ (CRD ↔ REST)    │ │  (native API)     │         │
│  └────────┬──────────┘ └────────┬────────┘ └────────┬──────────┘         │
└───────────┼─────────────────────┼───────────────────┼────────────────────┘
            │                     │                   │
            ▼                     ▼                   ▼
    ┌───────────────┐   ┌──────────────┐    ┌──────────────────┐
    │  E2B Cloud    │   │  K8s Cluster │    │  OpenSandbox     │
    │  (SaaS API)   │   │  + CRDs      │    │  Runtime         │
    └───────────────┘   └──────────────┘    └──────────────────┘
```

### Key Value Propositions

| Stakeholder | Benefit |
|---|---|
| **AI App Developers** | Write once, run against any sandbox backend — zero code changes |
| **Platform Teams** | Swap backends (cloud ↔ self-hosted) without rewriting integrations |
| **Framework Authors** | Single integration point instead of per-backend adapters |
| **Enterprises** | Meet data sovereignty / compliance by routing to self-hosted runtimes |

---

## 2. Problem Statement

### 2.1 Runtime Fragmentation

The AI agent sandbox ecosystem is fragmented across multiple incompatible runtimes:

| Runtime | Type | API Protocol | Self-Hostable |
|---|---|---|---|
| **E2B Cloud** | SaaS | E2B REST + WS | No |
| **agent-sandbox** | K8s CRD-based | K8s API (CRUD on CRDs) | Yes |
| **OpenSandbox** | Container-based | Custom REST API | Yes |
| **Daytona** | Dev environment | Daytona API | Yes |
| **Modal** | Serverless compute | Modal SDK | No |

### 2.2 SDK Ecosystem Lock-In

E2B has become the de-facto standard API for AI agent sandboxing. Major frameworks integrate exclusively with E2B SDKs:
- LangChain / LangGraph
- AutoGPT
- OpenAI Agents SDK
- CrewAI
- Dify
- MetaGPT

Teams with compliance, data sovereignty, or latency requirements **cannot self-host E2B**, and must either:
1. Rewrite their entire codebase to use a different SDK
2. Maintain fragile custom adapters per backend

### 2.3 The Missing Layer

There is no **universal abstraction gateway** that:
- Speaks the E2B protocol natively
- Routes to multiple heterogeneous backends
- Provides cross-cutting concerns (auth, rate limiting, observability)
- Enables dynamic backend selection per-request

---

## 3. Design Goals & Non-Goals

### Goals

| ID | Goal | Description |
|---|---|---|
| G1 | **Protocol Fidelity** | 100% compatibility with E2B Client HTTP + WebSocket protocols |
| G2 | **Backend Agnostic** | Pluggable adapter architecture supporting any sandbox runtime |
| G3 | **Zero-Code Migration** | Existing E2B SDK users connect to gateway with only URL change |
| G4 | **Production Ready** | Auth, rate limiting, audit logging, OTel metrics/tracing built-in |
| G5 | **Dynamic Routing** | Per-request backend selection based on tenant, template, or policy |
| G6 | **Horizontal Scalability** | Stateless design; scales via replica count |
| G7 | **Streaming Parity** | Full streaming support for code execution, terminals, port forwarding |

### Non-Goals

| ID | Non-Goal | Rationale |
|---|---|---|
| NG1 | Data-plane proxy for sandbox I/O | Out of scope — handled by backend-native data planes (e.g., KEP-1174 sandbox-gateway for agent-sandbox) |
| NG2 | Sandbox runtime implementation | Gateway translates APIs; it does not execute code or manage containers |
| NG3 | Custom SDK development | We leverage existing E2B SDKs; no new SDK needed |
| NG4 | Multi-cloud orchestration | Cross-region failover is deferred to infrastructure layer |

---

## 4. System Architecture

### 4.1 High-Level Components

```
┌─────────────────────────────────────────────────────────────────────┐
│                         E2BGateway Process                          │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │                    HTTP/WS Server (net/http)                │    │
│  │  ┌───────────────────────────────────────────────────────┐  │    │
│  │  │              Middleware Chain (request-scoped)        │  │    │
│  │  │                                                       │  │    │
│  │  │  RequestID → CORS → AuthN → AuthZ → RateLimit →       │  │    │
│  │  │  TenantContext → AuditLog → Router → BackendSelector  │  │    │
│  │  └───────────────────────────────────────────────────────┘  │    │
│  └─────────────────────────────────────────────────────────────┘    │
│                              │                                      │
│  ┌───────────────────────────┴───────────────────────────────┐      │
│  │                Protocol Translation Layer                 │      │
│  │                                                           │      │
│  │  ┌─────────────┐  ┌──────────────┐  ┌────────────────┐    │      │
│  │  │ E2B DTO     │  │ E2B DTO      │  │  Streaming     │    │      │
│  │  │ Translator  │  │ Validator    │  │  Normalizer    │    │      │
│  │  └─────────────┘  └──────────────┘  └────────────────┘    │      │
│  └───────────────────────────────────────────────────────────┘      │
│                              │                                      │
│  ┌───────────────────────────┴───────────────────────────────┐      │
│  │              Backend Adapter Registry                     │      │
│  │                                                           │      │
│  │  ┌──────────┐  ┌───────────────┐  ┌──────────────────┐    │      │
│  │  │ E2B Cloud│  │ agent-sandbox │  │  OpenSandbox     │    │      │
│  │  │ Adapter  │  │ Adapter       │  │  Adapter         │    │      │
│  │  └──────────┘  └───────────────┘  └──────────────────┘    │      │
│  └───────────────────────────────────────────────────────────┘      │
│                                                                     │
│  ┌───────────────────────────────────────────────────────────┐      │
│  │              Cross-Cutting Services                       │      │
│  │                                                           │      │
│  │  ┌──────────┐ ┌──────────┐ ┌───────┐ ┌───────────────┐    │      │
│  │  │ Auth     │ │ Rate     │ │ Cache │ │ OTel          │    │      │
│  │  │ Provider │ │ Limiter  │ │ Store │ │ Metrics/Trace │    │      │
│  │  └──────────┘ └──────────┘ └───────┘ └───────────────┘    │      │
│  └───────────────────────────────────────────────────────────┘      │
└─────────────────────────────────────────────────────────────────────┘
```

### 4.2 Request Lifecycle

```
E2B SDK Client
    │
    │  POST /api/v1/sandboxes  {"templateID": "code-interpreter", ...}
    │  Authorization: Bearer <api-key>
    ▼
┌─ Gateway HTTP Server ──────────────────────────────────────────────────┐
│                                                                        │
│  1. RequestID Middleware    → Generate X-Request-ID (UUID)             │
│  2. CORS Middleware         → Set CORS headers                         │
│  3. AuthN Middleware        → Validate API key / JWT                   │
│  4. AuthZ Middleware        → Check tenant permissions                 │
│  5. RateLimit Middleware    → Token bucket / sliding window check      │
│  6. TenantContext           → Attach tenant ID, quota info to context  │
│  7. AuditLog                → Log request metadata                     │
│  8. Router                  → Match path → Handler                     │
│  9. BackendSelector         → Choose adapter based on routing policy   │
│ 10. ProtocolTranslator      → Convert E2B DTO → Backend-native model  │
│ 11. Adapter.Execute()       → Call backend API                        │
│ 12. ResponseTranslator      → Convert backend response → E2B DTO      │
│ 13. OTel Span Export       → Record span, metrics                     │
│                                                                        │
│  Response: 201 Created  {"sandboxID": "abc123", ...}                  │
└────────────────────────────────────────────────────────────────────────┘
    │
    ▼
E2B SDK Client (receives standard E2B response)
```

### 4.3 Component Interaction Diagram

```
┌───────────┐     ┌──────────┐     ┌──────────────┐     ┌─────────────┐
│  Client   │────▶│  Router   │────▶│  Backend     │────▶│  Adapter    │
│  (SDK)    │     │          │     │  Selector    │     │  (pluggable)│
└───────────┘     └──────────┘     └──────────────┘     └──────┬──────┘
                              │                                  │
                              │          ┌──────────────┐        │
                              └─────────▶│  Strategy    │◀───────┘
                                         │  Config      │
                                         └──────────────┘

Backend Selector Strategies:
  - Static:    tenant → fixed backend
  - Template:  templateID → backend mapping
  - Weighted:  round-robin / weighted random across backends
  - Priority:  primary → fallback chain
  - Latency:   route to lowest-latency backend
```

---

## 5. API Surface

### 5.1 E2B-Compatible REST API

The gateway implements the complete E2B REST API specification:

#### Sandbox Lifecycle

| Method | Path | Description | Backend Mapping |
|---|---|---|---|
| `POST` | `/api/v1/sandboxes` | Create sandbox | Adapter-specific sandbox creation |
| `GET` | `/api/v1/sandboxes` | List running sandboxes | Adapter-specific list |
| `GET` | `/api/v1/sandboxes/{id}` | Get sandbox details | Adapter-specific get |
| `DELETE` | `/api/v1/sandboxes/{id}` | Kill/destroy sandbox | Adapter-specific delete |
| `POST` | `/api/v1/sandboxes/{id}/pause` | Pause sandbox | Adapter-specific pause |
| `POST` | `/api/v1/sandboxes/{id}/resume` | Resume sandbox | Adapter-specific resume |
| `PATCH` | `/api/v1/sandboxes/{id}/timeout` | Set sandbox timeout | Adapter-specific timeout update |
| `GET` | `/api/v1/sandboxes/{id}/logs` | Get sandbox logs | Adapter-specific log retrieval |

#### Code Execution

| Method | Path | Description | Backend Mapping |
|---|---|---|---|
| `POST` | `/api/v1/sandboxes/{id}/code` | Execute code (sync) | Exec into runtime |
| `POST` | `/api/v1/sandboxes/{id}/code/executions` | Start code execution (async) | Exec into runtime |
| `GET` | `/api/v1/sandboxes/{id}/code/executions/{execId}` | Get execution result | Query runtime |
| `DELETE` | `/api/v1/sandboxes/{id}/code/executions/{execId}` | Cancel execution | Signal runtime |

#### Commands

| Method | Path | Description | Backend Mapping |
|---|---|---|---|
| `POST` | `/api/v1/sandboxes/{id}/commands` | Run shell command | Exec into runtime |

#### Filesystem

| Method | Path | Description | Backend Mapping |
|---|---|---|---|
| `POST` | `/api/v1/sandboxes/{id}/files/write` | Write file | Filesystem API / exec |
| `GET` | `/api/v1/sandboxes/{id}/files/read` | Read file | Filesystem API / exec |
| `POST` | `/api/v1/sandboxes/{id}/files/upload` | Upload file | File transfer API |
| `GET` | `/api/v1/sandboxes/{id}/files/download` | Download file | File transfer API |
| `GET` | `/api/v1/sandboxes/{id}/files/list` | List directory | Filesystem API / exec |
| `POST` | `/api/v1/sandboxes/{id}/files/make-dir` | Create directory | Filesystem API / exec |
| `DELETE` | `/api/v1/sandboxes/{id}/files/remove` | Remove file/directory | Filesystem API / exec |

#### Templates

| Method | Path | Description | Backend Mapping |
|---|---|---|---|
| `GET` | `/api/v1/templates` | List templates | List template CRs / configs |
| `GET` | `/api/v1/templates/{id}` | Get template details | Get template CR / config |
| `POST` | `/api/v1/templates` | Create template | Create template CR / config |
| `DELETE` | `/api/v1/templates/{id}` | Delete template | Delete template CR / config |
| `POST` | `/api/v1/templates/{id}/build` | Trigger template build | Trigger build pipeline |
| `GET` | `/api/v1/templates/{id}/builds/{buildId}` | Get build status | Query build status |

#### Warm Pools

| Method | Path | Description | Backend Mapping |
|---|---|---|---|
| `GET` | `/api/v1/warm-pools` | List warm pools | List warm pool configs |
| `POST` | `/api/v1/warm-pools` | Create warm pool | Create warm pool config |
| `DELETE` | `/api/v1/warm-pools/{id}` | Delete warm pool | Delete warm pool config |

#### Health & Status

| Method | Path | Description |
|---|---|---|
| `GET` | `/healthz` | Gateway health check |
| `GET` | `/readyz` | Gateway readiness check |
| `GET` | `/metrics` | Prometheus metrics endpoint |

### 5.2 WebSocket Channels

| Channel | Path | Description |
|---|---|---|
| Code Execution Stream | `wss://.../sandboxes/{id}/code/ws` | Streaming code execution output |
| Terminal | `wss://.../sandboxes/{id}/terminal/ws` | Interactive terminal (PTY) |
| Port Forward | `wss://.../sandboxes/{id}/ports/{port}/ws` | Port forwarding |
| Filesystem Watch | `wss://.../sandboxes/{id}/files/watch/ws` | File change notifications |

### 5.3 Response Format

All responses follow the E2B response format:

```json
// Success - Single resource
{
  "sandboxID": "abc123def456",
  "templateID": "code-interpreter",
  "alias": "my-sandbox",
  "startedAt": "2026-07-27T10:00:00Z",
  "endAt": "2026-07-27T11:00:00Z",
  "status": "running",
  "metadata": {},
  "clientID": "client-xyz"
}

// Success - List
[
  { "sandboxID": "abc123", ... },
  { "sandboxID": "def456", ... }
]

// Error
{
  "code": 404,
  "detail": "Sandbox not found",
  "message": "Sandbox 'abc123' does not exist or has been destroyed"
}
```

### 5.4 Error Codes

| HTTP Status | E2B Error Code | Description |
|---|---|---|
| 400 | `InvalidRequest` | Malformed request body or parameters |
| 401 | `Unauthorized` | Missing or invalid API key |
| 403 | `Forbidden` | Insufficient permissions |
| 404 | `NotFound` | Resource not found |
| 408 | `Timeout` | Operation timed out |
| 409 | `Conflict` | Resource state conflict |
| 422 | `UnprocessableEntity` | Semantic validation error |
| 429 | `RateLimitExceeded` | Rate limit exceeded |
| 500 | `InternalServerError` | Internal gateway error |
| 502 | `BackendError` | Backend runtime error |
| 503 | `ServiceUnavailable` | Backend unavailable |
| 504 | `GatewayTimeout` | Backend timeout |

---

## 6. Backend Adapter Framework

### 6.1 Adapter Interface

```go
// SandboxAdapter defines the contract for all sandbox backend implementations.
// Each adapter translates gateway-level operations into backend-specific API calls.
type SandboxAdapter interface {
    // Name returns the unique identifier for this adapter (e.g., "e2b-cloud", "agent-sandbox", "opensandbox").
    Name() string

    // HealthCheck verifies connectivity and readiness of the backend.
    HealthCheck(ctx context.Context) error

    // --- Sandbox Lifecycle ---

    // CreateSandbox provisions a new sandbox instance.
    CreateSandbox(ctx context.Context, req *CreateSandboxRequest) (*Sandbox, error)

    // ListSandboxes returns all running sandboxes for the given tenant.
    ListSandboxes(ctx context.Context, opts ListOptions) ([]*Sandbox, error)

    // GetSandbox retrieves details of a specific sandbox.
    GetSandbox(ctx context.Context, sandboxID string) (*Sandbox, error)

    // KillSandbox terminates and destroys a sandbox.
    KillSandbox(ctx context.Context, sandboxID string) error

    // PauseSandbox suspends a sandbox (preserves state).
    PauseSandbox(ctx context.Context, sandboxID string) error

    // ResumeSandbox restores a paused sandbox.
    ResumeSandbox(ctx context.Context, sandboxID string) (*Sandbox, error)

    // SetTimeout updates the sandbox's auto-termination timer.
    SetTimeout(ctx context.Context, sandboxID string, timeout time.Duration) error

    // --- Code Execution ---

    // ExecuteCode runs code in the sandbox and returns results.
    ExecuteCode(ctx context.Context, sandboxID string, req *CodeExecutionRequest) (*CodeExecutionResult, error)

    // ExecuteCodeStream runs code and streams output via WebSocket.
    ExecuteCodeStream(ctx context.Context, sandboxID string, req *CodeExecutionRequest, stream CodeStream) error

    // RunCommand executes a shell command in the sandbox.
    RunCommand(ctx context.Context, sandboxID string, req *CommandRequest) (*CommandResult, error)

    // --- Filesystem ---

    // WriteFile writes content to a file in the sandbox.
    WriteFile(ctx context.Context, sandboxID string, req *FileWriteRequest) error

    // ReadFile reads file content from the sandbox.
    ReadFile(ctx context.Context, sandboxID string, path string) (*FileContent, error)

    // UploadFile uploads a file to the sandbox.
    UploadFile(ctx context.Context, sandboxID string, req *FileUploadRequest) error

    // DownloadFile downloads a file from the sandbox.
    DownloadFile(ctx context.Context, sandboxID string, path string) (io.ReadCloser, error)

    // ListFiles lists files in a directory.
    ListFiles(ctx context.Context, sandboxID string, path string) ([]FileInfo, error)

    // MakeDir creates a directory in the sandbox.
    MakeDir(ctx context.Context, sandboxID string, path string) error

    // RemoveFile removes a file or directory.
    RemoveFile(ctx context.Context, sandboxID string, path string) error

    // --- Templates ---

    // ListTemplates returns available sandbox templates.
    ListTemplates(ctx context.Context, opts ListOptions) ([]*Template, error)

    // GetTemplate retrieves template details.
    GetTemplate(ctx context.Context, templateID string) (*Template, error)

    // --- Terminal ---

    // CreateTerminal opens an interactive terminal session.
    CreateTerminal(ctx context.Context, sandboxID string, req *TerminalRequest) (*Terminal, error)

    // TerminalStream provides bidirectional terminal I/O.
    TerminalStream(ctx context.Context, sandboxID string, terminalID string, stream TerminalStream) error

    // --- Port Forwarding ---

    // GetPortURL returns the public URL for a sandbox port.
    GetPortURL(ctx context.Context, sandboxID string, port int) (string, error)
}
```

### 6.2 Adapter Registry

```go
// AdapterRegistry manages the lifecycle and selection of backend adapters.
type AdapterRegistry struct {
    adapters map[string]SandboxAdapter
    mu       sync.RWMutex
}

func (r *AdapterRegistry) Register(adapter SandboxAdapter) error
func (r *AdapterRegistry) Get(name string) (SandboxAdapter, bool)
func (r *AdapterRegistry) List() []SandboxAdapter
func (r *AdapterRegistry) HealthCheckAll(ctx context.Context) map[string]error
```

### 6.3 Backend Adapter: agent-sandbox

Maps E2B API calls to Kubernetes CRD operations:

| E2B API | agent-sandbox Operation |
|---|---|
| `POST /api/v1/sandboxes` | Create `SandboxClaim` referencing a `SandboxTemplate`, wait for Pod Ready |
| `GET /api/v1/sandboxes` | List `Sandbox` CRs in namespace |
| `GET /api/v1/sandboxes/{id}` | Get `Sandbox` CR + Pod status |
| `DELETE /api/v1/sandboxes/{id}` | Delete `Sandbox` CR (cascading pod deletion) |
| `POST /api/v1/sandboxes/{id}/pause` | Patch `Sandbox.spec.operatingMode: Paused` |
| `POST /api/v1/sandboxes/{id}/resume` | Patch `Sandbox.spec.operatingMode: Running` |
| `POST /api/v1/sandboxes/{id}/code` | Exec into Pod → runtime sidecar (KEP-539.2) |
| `POST /api/v1/sandboxes/{id}/commands` | Exec into Pod → runtime sidecar |
| `POST /api/v1/sandboxes/{id}/files/*` | Exec into Pod → filesystem sidecar API |
| `GET /api/v1/templates` | List `SandboxTemplate` CRs |

**Sandbox ID Mapping**: Generate E2B-compatible sandbox IDs (e.g., `abc123def456`) and store as annotation `e2b.e2bgateway.io/sandbox-id` on the `Sandbox` CR for bidirectional lookup.

### 6.4 Backend Adapter: E2B Cloud (Passthrough)

The E2B Cloud adapter acts as a **transparent proxy** to the official E2B SaaS API:

- All REST calls are forwarded to `https://api.e2b.dev/api/v1/...`
- API key is replaced with the configured E2B Cloud API key
- WebSocket connections are proxied with minimal overhead
- No translation needed — requests pass through with header rewriting

### 6.5 Backend Adapter: OpenSandbox

Maps E2B API calls to OpenSandbox's native REST API:

| E2B API | OpenSandbox API |
|---|---|
| `POST /api/v1/sandboxes` | `POST /sandboxes/create` |
| `GET /api/v1/sandboxes/{id}` | `GET /sandboxes/{id}/info` |
| `DELETE /api/v1/sandboxes/{id}` | `DELETE /sandboxes/{id}` |
| `POST /api/v1/sandboxes/{id}/code` | `POST /sandboxes/{id}/exec` |
| `POST /api/v1/sandboxes/{id}/commands` | `POST /sandboxes/{id}/exec` (shell mode) |
| File operations | `/sandboxes/{id}/fs/*` endpoints |

---

## 7. Control Plane vs Data Plane

### 7.1 Separation of Concerns

```
┌──────────────────────────────────────────────────────────────────┐
│                        CONTROL PLANE                             │
│  (E2BGateway handles directly)                                   │
│                                                                  │
│  - Sandbox CRUD lifecycle (create, list, get, kill)              │
│  - Template management                                           │
│  - Warm pool configuration                                       │
│  - Authentication & authorization                                │
│  - Rate limiting & quota enforcement                             │
│  - Audit logging                                                 │
│  - Routing & backend selection                                   │
│  - Metrics & tracing                                             │
└──────────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────────┐
│                        DATA PLANE                                │
│  (Routed to backend-native infrastructure)                       │
│                                                                  │
│  - Code execution streaming (long-lived WebSocket)               │
│  - Terminal I/O (bidirectional PTY)                              │
│  - Port forwarding                                               │
│  - File transfer (large uploads/downloads)                       │
│  - Filesystem watch events                                       │
│                                                                  │
│  For agent-sandbox: data plane handled by KEP-1174 gateway       │
│  For E2B Cloud: data plane proxied to E2B SaaS                   │
│  For OpenSandbox: data plane proxied to OpenSandbox runtime      │
└──────────────────────────────────────────────────────────────────┘
```

### 7.2 Data Plane Delegation for agent-sandbox

For the agent-sandbox backend, data-plane traffic (code execution, terminal, port forwarding) follows this path:

```
E2B SDK
   │
   │  WebSocket (code execution stream)
   ▼
E2BGateway
   │  Identifies sandbox → Pod mapping
   │  Routes to data-plane gateway
   ▼
┌─────────────────────────────────────┐
│  KEP-1174 sandbox-gateway (Envoy)   │
│  - Routes traffic to correct Pod    │
│  - Buffers during cold-start        │
│  - Triggers auto-resume             │
└──────────────┬──────────────────────┘
               │
               ▼
┌─────────────────────────────────────┐
│  In-Pod Runtime Sidecar (KEP-539.2) │
│  - execd (REST) or envd (gRPC)      │
│  - Code execution engine            │
│  - Filesystem API                   │
└─────────────────────────────────────┘
```

---

## 8. Streaming & WebSocket

### 8.1 Streaming Architecture

```
E2B SDK Client
    │
    │  WebSocket Upgrade
    │  GET /sandboxes/{id}/code/ws
    ▼
┌─ E2BGateway ──────────────────────────────────────────────┐
│                                                            │
│  WS Handler:                                               │
│  1. Upgrade HTTP → WebSocket                               │
│  2. Authenticate via query param or first frame            │
│  3. Look up sandbox → backend adapter                      │
│  4. Open backend-native stream:                            │
│     - E2B Cloud: proxy WS to E2B WS endpoint              │
│     - agent-sandbox: connect to sandbox-gateway (Envoy)    │
│     - OpenSandbox: connect to runtime WS endpoint          │
│  5. Bidirectional frame relay with normalization           │
│                                                            │
│  Frame Normalizer:                                         │
│  ┌────────────────────────────────────────────────┐       │
│  │ Backend-native frame → E2B standard frame      │       │
│  │                                                │       │
│  │ Agent output    → E2B stdout/stderr messages   │       │
│  │ Exit code       → E2B result message           │       │
│  │ Error events    → E2B error messages           │       │
│  │ Heartbeat       → E2B keepalive                │       │
│  └────────────────────────────────────────────────┘       │
│                                                            │
└────────────────────────────────────────────────────────────┘
```

### 8.2 E2B Streaming Message Types

```go
// E2B WebSocket message types for code execution
const (
    // Client → Server
    MsgTypeCodeExec     = "code/exec"      // Execute code snippet
    MsgTypeStdin        = "stdin"          // Send stdin input
    MsgTypeCancel       = "cancel"         // Cancel execution

    // Server → Client
    MsgTypeStdout       = "stdout"         // Standard output chunk
    MsgTypeStderr       = "stderr"         // Standard error chunk
    MsgTypeResult       = "result"         // Execution result (exit code, etc.)
    MsgTypeError        = "error"          // Execution error
    MsgTypeKeepAlive    = "keepAlive"      // Heartbeat
)
```

### 8.3 Streaming Frame Format

```json
// Code execution output frame (Server → Client)
{
  "type": "stdout",
  "data": {
    "content": "Hello from agent-sandbox!\n",
    "timestamp": "2026-07-27T10:00:01.123Z",
    "executionID": "exec-abc123"
  }
}

// Result frame
{
  "type": "result",
  "data": {
    "exitCode": 0,
    "executionID": "exec-abc123",
    "duration": 1.234
  }
}
```

### 8.4 Backpressure & Buffering

```
Client WS ←──── Buffered Channel (1024 frames) ──── Backend WS
                  │
                  ├─ If buffer full → apply backpressure to backend
                  ├─ If client disconnects → drain & cleanup
                  └─ If backend disconnects → send error frame to client
```

---

## 9. Authentication & Multi-Tenancy

### 9.1 Authentication Methods

```
┌─ Authentication Middleware ──────────────────────────────────┐
│                                                              │
│  Method 1: API Key (E2B-compatible)                          │
│  ┌────────────────────────────────────────────────────────┐ │
│  │  Header: X-API-Key: <api-key>                          │ │
│  │  Header: Authorization: Bearer <api-key>               │ │
│  │                                                        │ │
│  │  Validation:                                           │ │
│  │  1. Lookup key in Secret store (K8s Secret / Vault)    │ │
│  │  2. Verify not expired                                 │ │
│  │  3. Resolve tenant ID from key                         │ │
│  └────────────────────────────────────────────────────────┘ │
│                                                              │
│  Method 2: JWT Token (extended)                              │
│  ┌────────────────────────────────────────────────────────┐ │
│  │  Header: Authorization: Bearer <jwt>                   │ │
│  │                                                        │ │
│  │  Validation:                                           │ │
│  │  1. Verify signature (RS256/ES256)                     │ │
│  │  2. Check claims: exp, iss, aud                        │ │
│  │  3. Extract tenant ID from sub claim                   │ │
│  └────────────────────────────────────────────────────────┘ │
│                                                              │
│  Method 3: Mutual TLS (zero-trust)                           │
│  ┌────────────────────────────────────────────────────────┐ │
│  │  Client certificate → tenant identity                  │ │
│  │  CA bundle validation                                  │ │
│  └────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────┘
```

### 9.2 Multi-Tenant Model

```go
// Tenant represents an API consumer (team, organization, or user).
type Tenant struct {
    ID          string            `json:"id"`
    Name        string            `json:"name"`
    APIKeys     []APIKey          `json:"apiKeys"`
    Quotas      TenantQuotas      `json:"quotas"`
    Routing     RoutingPolicy     `json:"routing"`
    Backends    []BackendRef      `json:"backends"`    // Allowed backends
    Metadata    map[string]string `json:"metadata"`
}

// TenantQuotas defines resource limits per tenant.
type TenantQuotas struct {
    MaxConcurrentSandboxes int           `json:"maxConcurrentSandboxes"`
    MaxSandboxesPerDay     int           `json:"maxSandboxesPerDay"`
    MaxCPUPerSandbox       resource.Quantity `json:"maxCpuPerSandbox"`
    MaxMemoryPerSandbox    resource.Quantity `json:"maxMemoryPerSandbox"`
    MaxTimeoutPerSandbox   time.Duration `json:"maxTimeoutPerSandbox"`
    MaxRequestsPerMinute   int           `json:"maxRequestsPerMinute"`
}

// RoutingPolicy defines how a tenant's requests are routed.
type RoutingPolicy struct {
    Strategy       string            `json:"strategy"`       // static, template, weighted, priority
    DefaultBackend string            `json:"defaultBackend"`
    TemplateMap    map[string]string `json:"templateMap"`    // templateID → backendName
    Weights        map[string]int    `json:"weights"`        // backendName → weight
    FailoverChain  []string          `json:"failoverChain"`  // ordered backend list
}
```

### 9.3 Tenant Isolation

- **Namespace isolation** (agent-sandbox): Each tenant maps to a K8s namespace
- **Resource quotas**: Enforced at gateway level (before reaching backend)
- **Sandbox ID scoping**: Sandbox IDs are globally unique but tenant-scoped for lookups
- **Audit trail**: All operations logged with tenant context

---

## 10. Routing & Load Balancing

### 10.1 Routing Strategies

```go
// RoutingStrategy defines the interface for backend selection.
type RoutingStrategy interface {
    // SelectBackend chooses a backend for the given request context.
    SelectBackend(ctx context.Context, req *RoutingRequest) (string, error)
}
```

| Strategy | Description | Use Case |
|---|---|---|
| **Static** | Tenant → fixed backend | Single-backend deployments |
| **Template-based** | Template ID → backend mapping | Different backends for different sandbox types |
| **Weighted Round-Robin** | Distribute across backends by weight | Load distribution across clusters |
| **Priority/Failover** | Primary → secondary → tertiary | High availability with fallback |
| **Least-Connections** | Route to backend with fewest active sandboxes | Load balancing by utilization |
| **Latency-based** | Route to lowest-latency backend | Performance optimization |

### 10.2 Routing Decision Flow

```
Request arrives
    │
    ├─ Tenant has explicit routing policy?
    │   ├─ YES → Apply tenant policy (template map / weighted / priority)
    │   └─ NO → Apply global default routing
    │
    ├─ Backend healthy?
    │   ├─ YES → Route to selected backend
    │   └─ NO → Failover to next backend in chain
    │
    ├─ Backend within quota?
    │   ├─ YES → Proceed
    │   └─ NO → Return 429 or failover
    │
    └─ Return response
```

### 10.3 Health Checking

```go
// HealthChecker performs periodic health probes on backends.
type HealthChecker struct {
    interval    time.Duration
    timeout     time.Duration
    unhealthyThreshold int
    healthyThreshold   int
}

// Per-backend health status
type BackendHealth struct {
    Name        string
    Healthy     bool
    ConsecutiveFailures int
    ConsecutiveSuccesses int
    LastCheck   time.Time
    Latency     time.Duration
}
```

---

## 11. Observability

### 11.1 OpenTelemetry Integration

```
┌─ Observability Stack ──────────────────────────────────────────┐
│                                                                 │
│  Metrics (OTLP → Prometheus / Grafana)                          │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │  e2bgw_requests_total{method, path, backend, status}     │   │
│  │  e2bgw_request_duration_seconds{method, path, backend}   │   │
│  │  e2bgw_active_sandboxes{backend, tenant}                 │   │
│  │  e2bgw_websocket_connections{backend}                    │   │
│  │  e2bgw_backend_health{name, status}                      │   │
│  │  e2bgw_rate_limit_rejected_total{tenant}                 │   │
│  │  e2bgw_auth_failure_total{reason}                        │   │
│  │  e2bgw_streaming_bytes_sent{backend, direction}          │   │
│  └─────────────────────────────────────────────────────────┘   │
│                                                                 │
│  Tracing (OTLP → Jaeger / Tempo)                                │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │  Span: gateway.request                                   │   │
│  │    ├── Span: auth.validate                               │   │
│  │    ├── Span: ratelimit.check                             │   │
│  │    ├── Span: router.select_backend                       │   │
│  │    ├── Span: adapter.create_sandbox                      │   │
│  │    │     └── Span: k8s.create_sandboxclaim               │   │
│  │    │           └── Span: k8s.wait_pod_ready              │   │
│  │    └── Span: response.translate                          │   │
│  └─────────────────────────────────────────────────────────┘   │
│                                                                 │
│  Logging (Structured JSON → stdout / Loki)                      │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │  {"level":"info", "ts":"...", "request_id":"...",        │   │
│  │   "tenant":"...", "method":"POST", "path":"/api/v1/...", │   │
│  │   "backend":"agent-sandbox", "status":201,                │   │
│  │   "duration_ms":1234, "sandbox_id":"abc123"}              │   │
│  └─────────────────────────────────────────────────────────┘   │
│                                                                 │
│  Audit Log (Separate log stream)                                │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │  {"event":"sandbox.created", "tenant":"acme-corp",       │   │
│  │   "sandbox_id":"abc123", "backend":"agent-sandbox",       │   │
│  │   "template":"code-interpreter", "timestamp":"..."}       │   │
│  └─────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
```

### 11.2 Trace Context Propagation

- W3C Trace Context headers (`traceparent`, `tracestate`) propagated to backends
- For K8s backends: trace context injected as annotations on CRDs
- For proxied backends: headers forwarded transparently

---

## 12. Deployment Models

### 12.1 Standalone Deployment

```
┌───────────────────────────────────────────────┐
│  E2BGateway Deployment (3+ replicas)          │
│  ┌─────────┐  ┌─────────┐  ┌─────────┐        │
│  │ Pod 1   │  │ Pod 2   │  │ Pod 3   │        │
│  │ gateway │  │ gateway │  │ gateway │        │
│  └─────────┘  └─────────┘  └─────────┘        │
│       ▲              ▲              ▲         │
│       └──────────────┴──────────────┘         │
│                  │                            │
│           ┌──────┴──────┐                     │
│           │  Service /  │                     │
│           │  Ingress    │                     │
│           └──────┬──────┘                     │
└──────────────────┼────────────────────────────┘
                   │
           ┌───────┴────────┐
           │  Load Balancer │
           │  + TLS Term.   │
           └───────┬────────┘
                   │
           E2B SDK Clients
```

### 12.2 Sidecar to agent-sandbox Controller

```
┌─ K8s Node ───────────────────────────────-───────────┐
│                                                      │
│  ┌─ agent-sandbox-controller Pod ─────-────────────┐ │
│  │  ┌──────────────────┐  ┌────────────────────┐   │ │
│  │  │  controller      │  │  e2b-gateway       │   │ │
│  │  │  (reconciler)    │  │  (sidecar)         │   │ │
│  │  └──────────────────┘  └────────────────────┘   │ │
│  │         ▲                     │                 │ │
│  │         │    Shared K8s API   │                 │ │
│  │         └─────────────────────┘                 │ │
│  └─────────────────────────────────────────────────┘ │
│                                                      │
└──────────────────────────────────────────────────────┘
```

### 12.3 Envoy Gateway Integration

```
┌─ Envoy Gateway ──────────────────────────────────────┐
│                                                      │
│  Gateway (gateway.networking.k8s.io)                 │
│    ├── HTTPRoute: /api/v1/sandboxes/*                │
│    │     → backend: e2b-gateway Service              │
│    │       (control-plane routes)                    │
│    │                                                 │
│    ├── HTTPRoute: /sandboxes/{id}/code/ws            │
│    │     → backend: sandbox-gateway (Envoy ext_proc) │
│    │       (data-plane routes per KEP-1174)          │
│    │                                                 │
│    └── HTTPRoute: /healthz, /readyz, /metrics        │
│          → backend: e2b-gateway Service              │
│                                                      │
└──────────────────────────────────────────────────────┘
```

### 12.4 Helm Chart Values

```yaml
# values.yaml
replicaCount: 3

image:
  repository: ghcr.io/e2bgateway/e2bgateway
  tag: latest

service:
  type: ClusterIP
  port: 443
  targetPort: 8443

ingress:
  enabled: true
  className: nginx
  tls:
    - secretName: e2bgw-tls
      hosts:
        - sandbox.example.com

config:
  # Backend configurations
  backends:
    e2b-cloud:
      enabled: true
      type: e2b-cloud
      endpoint: https://api.e2b.dev
      apiKeySecret: e2b-cloud-api-key
    agent-sandbox:
      enabled: true
      type: agent-sandbox
      kubeconfig: ""  # in-cluster
      namespace: sandbox-system
    opensandbox:
      enabled: false
      type: opensandbox
      endpoint: http://opensandbox-runtime:8080

  # Routing
  routing:
    defaultBackend: agent-sandbox
    strategy: static

  # Auth
  auth:
    method: apikey  # apikey, jwt, mtls
    secretName: e2bgw-auth-keys

  # Rate limiting
  rateLimit:
    enabled: true
    defaultRPM: 100
    backend: memory  # memory, redis

  # Observability
  observability:
    otel:
      enabled: true
      endpoint: http://otel-collector:4317
    metrics:
      enabled: true
      path: /metrics
    auditLog:
      enabled: true

resources:
  requests:
    cpu: 100m
    memory: 128Mi
  limits:
    cpu: 500m
    memory: 512Mi

autoscaling:
  enabled: true
  minReplicas: 3
  maxReplicas: 20
  targetCPUUtilization: 70
```

---

## 13. Configuration

### 13.1 Configuration Hierarchy

```
Priority (highest → lowest):
1. CLI flags
2. Environment variables
3. Config file (YAML/JSON)
4. Default values
```

### 13.2 Config File Schema

```yaml
# e2bgateway-config.yaml
server:
  http:
    address: "0.0.0.0:8080"
    readTimeout: 30s
    writeTimeout: 300s   # long for streaming
    idleTimeout: 120s
  https:
    address: "0.0.0.0:8443"
    certFile: /etc/e2bgw/certs/tls.crt
    keyFile: /etc/e2bgw/certs/tls.key
  grpc:
    address: "0.0.0.0:9090"   # for OTLP receiver

backends:
  - name: e2b-cloud
    type: e2b-cloud
    config:
      endpoint: https://api.e2b.dev
      apiKeyRef: secret/e2b-cloud-key
      timeout: 60s
      maxRetries: 3

  - name: agent-sandbox
    type: agent-sandbox
    config:
      kubeconfig: ""        # empty = in-cluster
      namespace: sandbox-system
      sandboxClass: default
      defaultTemplate: code-interpreter
      timeout: 120s

  - name: opensandbox
    type: opensandbox
    config:
      endpoint: http://opensandbox:8080
      timeout: 60s

auth:
  providers:
    - type: apikey
      secretRef: e2bgw-api-keys
      headerName: X-API-Key
    - type: jwt
      issuer: https://auth.example.com
      jwksURL: https://auth.example.com/.well-known/jwks.json
      audience: e2bgateway

rateLimit:
  enabled: true
  backend: memory    # memory, redis
  defaultLimit:
    requestsPerMinute: 100
    burstSize: 20
  redis:
    address: redis:6379

routing:
  defaultBackend: agent-sandbox
  strategies:
    - name: tenant-static
      rules:
        - tenant: "acme-corp"
          backend: agent-sandbox
        - tenant: "beta-users"
          backend: e2b-cloud
    - name: template-routing
      rules:
        - template: "code-interpreter"
          backend: agent-sandbox
        - template: "browser"
          backend: opensandbox

observability:
  otel:
    serviceNamespace: e2bgateway
    endpoint: http://otel-collector:4317
    insecure: true
    samplingRatio: 1.0
  metrics:
    enabled: true
    path: /metrics
    prefix: e2bgw
  logging:
    level: info        # debug, info, warn, error
    format: json       # json, text
    auditLog: true
```

---

## 14. Security Model

### 14.1 Threat Model

```
┌─ Threat Boundaries ─────────────────────────────────────────────┐
│                                                                  │
│  T1: Client → Gateway (external)                                 │
│    - TLS termination required                                    │
│    - API key / JWT authentication                                │
│    - Rate limiting at edge                                       │
│    - Request size limits                                         │
│    - Input validation / sanitization                             │
│                                                                  │
│  T2: Gateway → Backend (internal)                                │
│    - mTLS between gateway and K8s API                            │
│    - Service account RBAC for CRD operations                     │
│    - Network policies restricting backend access                 │
│    - Backend API key rotation                                    │
│                                                                  │
│  T3: Sandbox Isolation                                           │
│    - Gateway does NOT break sandbox isolation                    │
│    - Code execution delegated to backend runtime                 │
│    - Gateway never executes user code directly                   │
│                                                                  │
│  T4: Data Plane                                                  │
│    - WebSocket connections authenticated                         │
│    - Port forwarding scoped per sandbox                          │
│    - File operations authorized per tenant                       │
└──────────────────────────────────────────────────────────────────┘
```

### 14.2 RBAC for agent-sandbox Backend

```yaml
# ClusterRole for E2BGateway when using agent-sandbox backend
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: e2bgateway-agent-sandbox
rules:
  # Sandbox lifecycle
  - apiGroups: ["agents.x-k8s.io"]
    resources: ["sandboxes", "sandboxclaims"]
    verbs: ["create", "get", "list", "watch", "delete", "patch"]
  - apiGroups: ["agents.x-k8s.io"]
    resources: ["sandboxtemplates"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["agents.x-k8s.io"]
    resources: ["sandboxwarmpools"]
    verbs: ["get", "list", "watch"]
  # Pod exec for code execution
  - apiGroups: [""]
    resources: ["pods/exec"]
    verbs: ["create"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch"]
  # Secret access for sandbox ID mapping
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get", "list"]
```

---

## 15. Performance & Scalability

### 15.1 Performance Targets

| Metric | Target | Notes |
|---|---|---|
| Control plane latency (create) | < 2s (excluding sandbox startup) | Gateway overhead only |
| Control plane latency (get/list) | < 50ms | Cached where possible |
| Data plane latency (WS relay) | < 5ms | Frame relay overhead |
| WebSocket connections per pod | 10,000+ | Tunable via file descriptors |
| Requests per second per pod | 5,000+ | Control plane operations |
| Memory per pod | < 256MB base | + streaming buffer |

### 15.2 Caching Strategy

```
┌─ Cache Layers ─────────────────────────────────────────────────┐
│                                                                 │
│  L1: In-Memory (per-pod, LRU)                                  │
│    - Sandbox metadata (TTL: 30s)                                │
│    - Template list (TTL: 60s)                                   │
│    - Tenant config (TTL: 5min)                                  │
│    - Backend health status (TTL: 10s)                           │
│                                                                 │
│  L2: Redis (shared, optional)                                   │
│    - Rate limit counters                                        │
│    - Sandbox ID → backend mapping                               │
│    - Tenant quota usage                                         │
│    - Cross-pod cache coherence                                  │
│                                                                 │
│  Cache Invalidation:                                            │
│    - Write-through for mutations                                │
│    - TTL-based expiry for reads                                 │
│    - Watch-based for K8s resources                              │
└─────────────────────────────────────────────────────────────────┘
```

### 15.3 Connection Pooling

```go
// Per-backend connection pool configuration
type PoolConfig struct {
    MaxIdleConns        int           `yaml:"maxIdleConns"`        // 100
    MaxIdleConnsPerHost int           `yaml:"maxIdleConnsPerHost"` // 50
    MaxConnsPerHost     int           `yaml:"maxConnsPerHost"`     // 200
    IdleConnTimeout     time.Duration `yaml:"idleConnTimeout"`     // 90s
    DisableKeepAlives   bool          `yaml:"disableKeepAlives"`   // false
}
```

---

## 16. Migration Strategy

### 16.1 From E2B Cloud to Self-Hosted (Gradual)

```
Phase 1: Shadow Mode
  - All requests go to E2B Cloud (primary)
  - Gateway mirrors writes to agent-sandbox (shadow)
  - Compare responses, log discrepancies
  - No client-visible changes

Phase 2: Read Migration
  - GET requests routed to agent-sandbox
  - POST/DELETE still go to E2B Cloud
  - Verify data consistency

Phase 3: Write Migration (canary)
  - 10% of tenants routed to agent-sandbox
  - Monitor error rates, latency
  - Gradually increase: 10% → 25% → 50% → 100%

Phase 4: Full Migration
  - All traffic to agent-sandbox
  - E2B Cloud as fallback only
  - Decommission E2B Cloud dependency
```

### 16.2 Zero-Code SDK Migration

```python
# Before: Using E2B Cloud directly
import os
os.environ["E2B_API_KEY"] = "e2b_cloud_key"
from e2b_code_interpreter import Sandbox
sbx = Sandbox.create(template="code-interpreter")

# After: Point at E2BGateway (NO OTHER CHANGES)
import os
os.environ["E2B_API_URL"] = "https://sandbox.example.com"  # ← Only change
os.environ["E2B_API_KEY"] = "gateway_api_key"
from e2b_code_interpreter import Sandbox
sbx = Sandbox.create(template="code-interpreter")  # Same API, different backend
```

---

## 17. Project Structure

```
e2bgateway/
├── cmd/
│   └── e2bgateway/
│       └── main.go                      # Entry point
├── internal/
│   ├── server/
│   │   ├── http.go                      # HTTP server setup
│   │   ├── websocket.go                 # WebSocket upgrade handler
│   │   └── middleware/
│   │       ├── requestid.go             # X-Request-ID generation
│   │       ├── cors.go                  # CORS handling
│   │       ├── auth.go                  # Authentication
│   │       ├── ratelimit.go             # Rate limiting
│   │       ├── audit.go                 # Audit logging
│   │       └── recovery.go              # Panic recovery
│   ├── api/
│   │   ├── v1/
│   │   │   ├── sandbox.go              # Sandbox CRUD handlers
│   │   │   ├── code.go                 # Code execution handlers
│   │   │   ├── command.go              # Command execution handlers
│   │   │   ├── files.go                # Filesystem handlers
│   │   │   ├── template.go             # Template handlers
│   │   │   ├── warmpool.go             # Warm pool handlers
│   │   │   └── health.go              # Health check handlers
│   │   ├── dto/                        # E2B wire format types
│   │   │   ├── sandbox.go
│   │   │   ├── code.go
│   │   │   ├── files.go
│   │   │   ├── template.go
│   │   │   └── errors.go
│   │   └── router.go                   # Route registration
│   ├── adapter/
│   │   ├── interface.go                # SandboxAdapter interface
│   │   ├── registry.go                 # Adapter registry
│   │   ├── types.go                    # Shared adapter types
│   │   ├── e2bcloud/
│   │   │   ├── adapter.go             # E2B Cloud passthrough
│   │   │   ├── client.go              # E2B API client
│   │   │   └── translator.go          # Request/response translation
│   │   ├── agentsandbox/
│   │   │   ├── adapter.go             # agent-sandbox CRD adapter
│   │   │   ├── crd_client.go          # K8s CRD client
│   │   │   ├── translator.go          # E2B ↔ CRD translation
│   │   │   ├── exec.go               # Pod exec for code execution
│   │   │   └── watcher.go            # CRD watch for status updates
│   │   └── opensandbox/
│   │       ├── adapter.go             # OpenSandbox adapter
│   │       ├── client.go              # OpenSandbox API client
│   │       └── translator.go          # Request/response translation
│   ├── routing/
│   │   ├── router.go                  # Main routing logic
│   │   ├── strategy.go                # Routing strategy interface
│   │   ├── static.go                  # Static routing
│   │   ├── template_based.go          # Template-based routing
│   │   ├── weighted.go               # Weighted round-robin
│   │   ├── priority.go               # Priority/failover
│   │   └── health.go                 # Backend health checker
│   ├── auth/
│   │   ├── provider.go                # Auth provider interface
│   │   ├── apikey.go                  # API key authentication
│   │   ├── jwt.go                     # JWT authentication
│   │   ├── mtls.go                    # Mutual TLS authentication
│   │   └── tenant.go                  # Tenant resolution
│   ├── ratelimit/
│   │   ├── limiter.go                 # Rate limiter interface
│   │   ├── token_bucket.go            # Token bucket implementation
│   │   ├── sliding_window.go          # Sliding window implementation
│   │   └── redis.go                   # Redis-backed limiter
│   ├── streaming/
│   │   ├── ws_handler.go             # WebSocket handler
│   │   ├── frame.go                   # Frame types and parsing
│   │   ├── normalizer.go             # Frame normalization
│   │   ├── relay.go                   # Bidirectional relay
│   │   └── buffer.go                  # Backpressure buffer
│   ├── cache/
│   │   ├── cache.go                   # Cache interface
│   │   ├── memory.go                  # In-memory LRU cache
│   │   └── redis.go                   # Redis cache
│   ├── config/
│   │   ├── config.go                  # Config loading & validation
│   │   └── schema.go                  # Config schema types
│   └── observability/
│       ├── otel.go                    # OpenTelemetry setup
│       ├── metrics.go                 # Metric definitions
│       ├── tracer.go                  # Tracer setup
│       └── audit.go                   # Audit logger
├── pkg/
│   ├── api/                           # Public API types (importable)
│   │   ├── types.go
│   │   └── constants.go
│   └── client/                        # Gateway admin client
│       └── client.go
├── deploy/
│   ├── helm/
│   │   └── e2bgateway/
│   │       ├── Chart.yaml
│   │       ├── values.yaml
│   │       └── templates/
│   │           ├── deployment.yaml
│   │           ├── service.yaml
│   │           ├── ingress.yaml
│   │           ├── configmap.yaml
│   │           ├── secret.yaml
│   │           ├── serviceaccount.yaml
│   │           ├── clusterrole.yaml
│   │           ├── clusterrolebinding.yaml
│   │           └── hpa.yaml
│   └── kustomize/
│       ├── base/
│       │   ├── kustomization.yaml
│       │   ├── deployment.yaml
│       │   └── service.yaml
│       └── overlays/
│           ├── dev/
│           ├── staging/
│           └── production/
├── configs/
│   ├── e2bgateway-default.yaml        # Default configuration
│   └── e2bgateway-example.yaml        # Example with all backends
├── docs/
│   ├── design/
│   │   └── README.md                  # This document
│   ├── api/
│   │   └── openapi.yaml               # OpenAPI 3.0 specification
│   └── guides/
│       ├── getting-started.md
│       ├── backend-setup.md
│       └── migration.md
├── test/
│   ├── e2e/
│   │   ├── sandbox_lifecycle_test.go
│   │   ├── code_execution_test.go
│   │   ├── streaming_test.go
│   │   └── multi_backend_test.go
│   ├── integration/
│   │   ├── adapter_test.go
│   │   └── routing_test.go
│   └── mock/
│       ├── backend.go                 # Mock backend adapter
│       └── k8s_client.go             # Mock K8s client
├── hack/
│   ├── boilerplate.go.txt
│   └── update-codegen.sh
├── Dockerfile
├── Makefile
├── go.mod
├── go.sum
├── README.md
└── LICENSE
```

---

## 18. Implementation Phases

### Phase 1: Foundation (Weeks 1-3)

| Task | Description | Deliverable |
|---|---|---|
| F1 | Go module, project structure, Makefile | Buildable skeleton |
| F2 | Config loading (YAML + env + flags) | Config system |
| F3 | HTTP server with middleware chain | Running server |
| F4 | E2B DTO types and validator | Wire format types |
| F5 | SandboxAdapter interface | Contract definition |
| F6 | Adapter registry | Dynamic registration |
| F7 | OTel metrics + tracing setup | Observability baseline |

### Phase 2: E2B Cloud Adapter (Weeks 4-5)

| Task | Description | Deliverable |
|---|---|---|
| E1 | E2B Cloud API client | HTTP client |
| E2 | Sandbox CRUD translation | Create/List/Get/Kill |
| E3 | Code execution passthrough | Execute/run |
| E4 | File operations passthrough | Read/write/upload |
| E5 | Template operations | List/get templates |
| E6 | WebSocket proxy for E2B Cloud | Streaming proxy |

### Phase 3: agent-sandbox Adapter (Weeks 6-9)

| Task | Description | Deliverable |
|---|---|---|
| A1 | K8s client setup (client-go / controller-runtime) | K8s connectivity |
| A2 | SandboxClaim creation + wait for ready | Create sandbox |
| A3 | Sandbox list/get/delete via CRD queries | Sandbox CRUD |
| A4 | Pause/resume via operatingMode patch | Lifecycle mgmt |
| A5 | Pod exec for code execution | Code execution |
| A6 | Pod exec for file operations | File I/O |
| A7 | SandboxTemplate CRD mapping | Template support |
| A8 | Sandbox ID annotation management | ID mapping |
| A9 | CRD watcher for status updates | Async status |

### Phase 4: Auth & Multi-Tenancy (Weeks 10-11)

| Task | Description | Deliverable |
|---|---|---|
| T1 | API key auth provider | Basic auth |
| T2 | JWT auth provider | Token auth |
| T3 | Tenant context propagation | Multi-tenancy |
| T4 | Rate limiting (memory backend) | Rate limit |
| T5 | Rate limiting (Redis backend) | Distributed limit |
| T6 | Audit logging | Audit trail |

### Phase 5: Routing & Load Balancing (Weeks 12-13)

| Task | Description | Deliverable |
|---|---|---|
| R1 | Static routing strategy | Basic routing |
| R2 | Template-based routing | Smart routing |
| R3 | Weighted round-robin | Load distribution |
| R4 | Priority/failover chain | HA routing |
| R5 | Backend health checker | Health monitoring |
| R6 | Dynamic config reload | Hot reload |

### Phase 6: Streaming & WebSocket (Weeks 14-15)

| Task | Description | Deliverable |
|---|---|---|
| S1 | WebSocket upgrade handler | WS support |
| S2 | Frame normalization layer | Protocol translation |
| S3 | Bidirectional relay | WS proxy |
| S4 | Terminal streaming | Terminal support |
| S5 | Port forwarding | Port proxy |
| S6 | Backpressure management | Stability |

### Phase 7: OpenSandbox Adapter + Polish (Weeks 16-18)

| Task | Description | Deliverable |
|---|---|---|
| O1 | OpenSandbox API client | API client |
| O2 | Sandbox CRUD translation | Full CRUD |
| O3 | Code execution + file ops | Data plane |
| P1 | Helm chart | Deployment |
| P2 | E2E test suite | Quality |
| P3 | Documentation | User guides |
| P4 | Performance testing & tuning | Production ready |

---

## Appendix A: E2B SDK Compatibility Matrix

| SDK | Version | Compatibility | Notes |
|---|---|---|---|
| e2b (Python) | >= 0.16 | Full | Primary target |
| e2b-code-interpreter (Python) | >= 0.4 | Full | Code execution |
| @e2b/cli (JS) | >= 0.1 | Full | CLI tools |
| e2b (JS/TS) | >= 0.15 | Full | Primary target |
| LangChain E2B integration | latest | Full | Via E2B SDK |
| CrewAI sandbox tool | latest | Full | Via E2B SDK |

## Appendix B: Comparison with Related Work

| Feature | E2BGateway | openkruise/agents | KEP-1174 sandbox-gateway |
|---|---|---|---|
| E2B API compatibility | Full | Partial | N/A |
| Multi-backend support | Yes (E2B, agent-sandbox, OpenSandbox) | agent-sandbox only | N/A |
| Data-plane proxy | Delegate to backend | N/A | Yes (Envoy) |
| Auth & multi-tenancy | Built-in | K8s native | N/A |
| Rate limiting | Built-in | K8s native | N/A |
| Observability | OTel built-in | Prometheus | N/A |
| Routing strategies | Multiple | None | Path-based |
| Streaming normalization | Yes | N/A | Protocol-level |

---

## Appendix C: E2B API Endpoint Reference (Complete)

Based on research of E2B SDK source code and documentation. The gateway must implement all of these endpoints for full SDK compatibility.

> **Note**: The exact paths and field names may evolve. Verify against the live E2B OpenAPI spec and SDK source code during implementation.

### Sandbox Endpoints

| Method | Path | Description |
|---|---|---|
| `POST` | `/sandboxes` | Create sandbox (202 Accepted) |
| `GET` | `/sandboxes` | List running sandboxes |
| `GET` | `/sandboxes/{id}` | Get sandbox details |
| `DELETE` | `/sandboxes/{id}` | Kill sandbox (204 No Content) |
| `POST` | `/sandboxes/{id}/timeout` | Set sandbox timeout |
| `POST` | `/sandboxes/{id}/pause` | Pause sandbox (204 No Content) |
| `POST` | `/sandboxes/{id}/resume` | Resume sandbox (202 Accepted) |
| `POST` | `/sandboxes/{id}/snapshots` | Create snapshot (201 Created) |
| `GET` | `/sandboxes/{id}/snapshots` | List snapshots |
| `POST` | `/sandboxes/{id}/access-token` | Get scoped access token for WebSocket |

### Filesystem Endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/sandboxes/{id}/files?path={path}` | Read file |
| `POST` | `/sandboxes/{id}/files` | Write file |
| `POST` | `/sandboxes/{id}/files/upload` | Upload file (multipart) |
| `GET` | `/sandboxes/{id}/files/download?path={path}` | Download file |
| `POST` | `/sandboxes/{id}/files/list` | List directory |
| `POST` | `/sandboxes/{id}/files/make-dir` | Create directory |
| `POST` | `/sandboxes/{id}/files/remove` | Remove file/directory |

### Process/Command Endpoints

| Method | Path | Description |
|---|---|---|
| `POST` | `/sandboxes/{id}/commands` | Execute command (sync) |
| `GET` | `/sandboxes/{id}/processes` | List running processes |
| `POST` | `/sandboxes/{id}/processes/{pid}/kill` | Kill process |
| `POST` | `/sandboxes/{id}/processes/{pid}/stdin` | Send stdin to process |

### Port Forwarding

| Method | Path | Description |
|---|---|---|
| `GET` | `/sandboxes/{id}/ports` | Get open ports |
| `GET` | `/sandboxes/{id}/ports/{port}` | Get port public URL |

### Template Endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/templates` | List templates |
| `POST` | `/templates` | Start template build (202 Accepted) |
| `GET` | `/templates/{id}` | Get template |
| `DELETE` | `/templates/{id}` | Delete template |
| `POST` | `/templates/{id}/builds` | Trigger new build |
| `POST` | `/templates/{id}/builds/{bid}/status` | Get build status |
| `POST` | `/templates/{id}/aliases` | Create alias |
| `DELETE` | `/templates/{id}/aliases/{alias}` | Delete alias |

### Warm Pool Endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/warm-pools` | List warm pools |
| `POST` | `/warm-pools` | Create warm pool |
| `GET` | `/warm-pools/{id}` | Get warm pool |
| `DELETE` | `/warm-pools/{id}` | Delete warm pool |
| `POST` | `/warm-pools/{id}/size` | Update pool size |

### WebSocket Protocol (envd daemon)

Connection URL: `wss://{sandboxID}-{port}.envs.e2b.dev/ws?access_token={token}`

**Client → Server message types:**
- `command` — Execute command with streaming output
- `terminal:start` / `terminal:input` / `terminal:resize` — PTY session
- `fs:watch` — Filesystem change notifications

**Server → Client message types:**
- `stdout` / `stderr` — Streaming output
- `exit` — Process exit with code
- `terminal:data` — PTY output
- `fs:event` — File change events
- `error` — Error with code and message

---

## Appendix D: agent-sandbox CRD Model

Based on research (low confidence — verify against actual repo before implementation).

### Core CRDs

| CRD | Purpose | Key Fields |
|---|---|---|
| `Sandbox` | Per-instance resource | `spec.operatingMode` (Running/Paused), `status.phase`, pod reference |
| `SandboxTemplate` | Reusable class (like PodTemplate) | Pod spec skeleton, runtime sidecar spec, volume mounts |
| `SandboxClaim` | Claim-style (like PVC) | Template match, resource requests → binds to pool or provisions |
| `SandboxWarmPool` | Pre-warmed instances | `size`, `templateRef`, maintains ready-to-bind sandboxes |

### Lifecycle Flow

```
SandboxClaim → bind-to-pool OR provision → Sandbox (from SandboxTemplate)
    → Pod created with runtime-sidecar → sidecar Start → status.phase=Running

Pause: controller calls sidecar Pause → sets spec.operatingMode=Paused
Resume: reverse
Destroy: claim released → ownerRef cascade → Pod deletion → sidecar Stop
```

### KEP References

| KEP | Purpose |
|---|---|
| KEP-539.2 | Runtime standardization — in-pod runtime sidecar (gRPC/Unix socket API) |
| KEP-1174 | Data-plane gateway — Envoy-based traffic routing to sandbox pods |

### E2BGateway ↔ agent-sandbox Mapping

| E2B API | agent-sandbox Operation |
|---|---|
| `POST /sandboxes` | Create `SandboxClaim` → wait for Pod Ready |
| `GET /sandboxes` | List `Sandbox` CRs |
| `GET /sandboxes/{id}` | Get `Sandbox` CR + Pod status (via `e2b.e2bgateway.io/sandbox-id` annotation) |
| `DELETE /sandboxes/{id}` | Delete `Sandbox` CR (cascading) |
| `POST /sandboxes/{id}/pause` | Patch `Sandbox.spec.operatingMode: Paused` |
| `POST /sandboxes/{id}/resume` | Patch `Sandbox.spec.operatingMode: Running` |
| `POST /sandboxes/{id}/commands` | `kubectl exec` → Pod → runtime sidecar (KEP-539.2) |
| `GET/POST /sandboxes/{id}/files/*` | `kubectl exec` → Pod → filesystem sidecar API |
| `GET /templates` | List `SandboxTemplate` CRs |

---

## Appendix E: Research Notes & Caveats

1. **E2B API paths**: The official E2B API may use `/sandboxes` (no `/api/v1` prefix) while the agent-sandbox issue #1154 proposes `/api/v1/sandboxes`. E2BGateway should support **configurable path prefix** to handle both conventions.

2. **OpenSandbox**: No well-known open-source project specifically named "OpenSandbox" was found in the AI agent sandbox space. The adapter is kept as a **generic/pluggable template** that can be implemented when the target backend is finalized. The `SandboxAdapter` interface is designed to accommodate any backend.

3. **agent-sandbox CRD field names**: The field names in Appendix D are from training data and should be verified against the actual CRD YAML definitions in `kubernetes-sigs/agent-sandbox` before implementation.

4. **E2B SDK as ground truth**: The E2B Python SDK (`e2b-dev/e2b-python`) and JS SDK (`e2b-dev/e2b-js`) contain the authoritative client implementations. Their generated API clients show exact request/response schemas.
