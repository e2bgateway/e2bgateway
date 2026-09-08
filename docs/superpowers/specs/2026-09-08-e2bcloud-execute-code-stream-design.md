# E2B Cloud: ExecuteCodeStream Streaming Implementation

**Issue**: [#25](https://github.com/e2bgateway/e2bgateway/issues/25) — [M1-R1.1] E2B Cloud: ExecuteCodeStream Streaming Execution
**Date**: 2026-09-08
**Priority**: P0 — Blocks interactive development
**Approach**: A — Integrate WSProxy + streaming.Normalizer for standardized streaming output

---

## 1. Problem Statement

The E2B Cloud adapter's `ExecuteCodeStream()` returns `streaming not yet implemented`, blocking interactive development workflows. As a P0 feature parity task, this requires implementing true streaming code execution compatible with the E2B Client SDK protocol.

## 2. Design Goals

- **Protocol parity**: Fully compatible with the E2B Client SDK protocol, supporting both ConnectRPC and WebSocket modes
- **Streaming output**: Integrate WSProxy for standardized streaming code execution output
- **Frame standardization**: Use `streaming.Normalizer` to ensure all output frames conform to the E2B SDK format
- **Graceful degradation**: Automatically fall back to synchronous execution mode when WebSocket is unavailable
- **Context cancellation**: Support ctx cancellation with proper goroutine and connection cleanup

## 3. Architecture

### 3.1 Dual-Mode Protocol Compatibility

```
E2B SDK Client
      │
      ├── ConnectRPC (data plane — existing)
      │   └── {port}-{sandboxID}.{sandboxDomain} -> envd:49983
      │       -> gateway catch-all proxy -> httputil.ReverseProxy
      │       (supports server-stream RPC, already working)
      │
      └── WebSocket (new)
          └── /sandboxes/{sandboxID}/ws -> ExecuteCodeStreamHandler
              -> adapter.ExecuteCodeStream()
              -> CodeStreamer.Stream()
              -> envd WebSocket
```

### 3.2 Component Relationships

```
internal/adapter/e2bcloud/
├── adapter.go       — Adapter.ExecuteCodeStream() implementation
│                     Calls CodeStreamer, falls back on WS failure
├── streamer.go      — CodeStreamer: connects to envd WS, processes frames
│                     Uses streaming.Normalizer for standardization
├── websocket.go     — WSProxy: existing, for gateway-level WS proxying
└── client.go        — E2B Cloud HTTP client

internal/api/v1/
└── ws_handler.go    — ExecuteCodeStreamHandler: WS handler
                        Bridges WS frames <-> CodeStream interface

internal/streaming/
├── frame.go         — Frame types and constructors
├── normalizer.go    — Normalizer: output frame standardization
└── ...              — Buffer, Relay, WSHandler, etc.
```

### 3.3 Data Flow

```
SDK Client                Gateway                     E2B Cloud envd
    │                        │                              │
    │── WS Upgrade ─────────>│                              │
    │                        │── GetAccessToken ────────────>│
    │                        │<── access_token ──────────────│
    │                        │── Dial WS ───────────────────>│
    │<── 101 Switching ─────│                              │
    │                        │                              │
    │── code/exec ──────────>│                              │
    │                        │── code/exec frame ──────────>│
    │                        │                              │
    │                        │<── stdout frame ─────────────│
    │<── stdout frame ──────│                              │
    │                        │<── stderr frame ─────────────│
    │<── stderr frame ──────│                              │
    │                        │<── result frame ─────────────│
    │<── result frame ──────│                              │
    │<── keepAlive ─────────│                              │
```

## 4. API Surface

### 4.1 WebSocket Endpoint

```
GET /sandboxes/{sandboxID}/ws
Headers:
  X-Access-Token: <token>  (or ?access_token=<token>)

WebSocket frames:
  Client -> Server:
    {"type":"code/exec","data":{"code":"...","language":"python"}}
    {"type":"cancel"}
    {"type":"keepAlive"}

  Server -> Client:
    {"type":"stdout","data":{"content":"...","timestamp":"...","executionID":"..."}}
    {"type":"stderr","data":{"content":"...","timestamp":"...","executionID":"..."}}
    {"type":"result","data":{"exitCode":0,"duration":1.23}}
    {"type":"error","data":{"code":"runtime_error","message":"..."}}
    {"type":"keepAlive"}
```

### 4.2 Adapter Interface

No interface changes. `SandboxAdapter.ExecuteCodeStream()` signature remains unchanged.

## 5. Configuration

No new configuration fields.

## 6. Security Considerations

- **Access token authentication**: WS connections validated via `ValidateAccessToken` before upgrade
- **No shell injection risk**: Code content is transmitted via WS frames, never concatenated into shell commands
- **Write deadline**: WebSocket write operations have a 30-second timeout
- **Context propagation**: ctx cancellation closes the WS connection, unblocking `ReadMessage()`
- **Frame size**: gorilla/websocket default read buffer is 4096 bytes

## 7. Observability

- Structured logs include `sandboxID`, `executionID`, `adapter`
- Execution duration exposed via `result` frame's `duration` field

## 8. Testing Strategy

| Layer | Coverage |
|-------|----------|
| Unit | CodeStreamer: WS connection, frame handling, context cancellation, connection refused |
| Unit | wsCodeStream: frame standardization, Send/Close |
| Handler | WS upgrade, code/exec frame, missing sandbox ID |
| Integration | Adapter fallback (WS unavailable -> synchronous) |
| E2E | Existing envd proxy token enforcement tests cover the path |

## 9. Degradation Strategy

```go
func (a *Adapter) ExecuteCodeStream(...) error {
    err := streamer.Stream(...)  // Try WS streaming
    if err != nil && isWebSocketNotAvailable(err) {
        return a.streamFromSync(...)  // Fall back to synchronous
    }
    return err
}
```

Degradation triggers: `dial`, `connection refused`, `no such host`, `bad handshake`, `context canceled`

## 10. File Manifest

| File | Action |
|------|--------|
| `internal/adapter/e2bcloud/streamer.go` | New |
| `internal/adapter/e2bcloud/streamer_test.go` | New |
| `internal/adapter/e2bcloud/adapter.go` | Modified |
| `internal/api/v1/ws_handler.go` | New |
| `internal/api/v1/ws_handler_test.go` | New |
| `internal/server/http.go` | Modified |

## 11. Acceptance Criteria

- [x] `go test ./... -race` all pass
- [x] `golangci-lint run` zero warnings
- [x] `go vet ./...` passes
- [x] ExecuteCodeStream output frames are E2B SDK protocol compatible
- [x] Automatic fallback to synchronous mode when WS is unavailable
- [x] Context cancellation properly cleans up goroutines and connections
