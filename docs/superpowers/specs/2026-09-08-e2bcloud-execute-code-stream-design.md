# E2B Cloud: ExecuteCodeStream 流式执行

**Issue**: [#25](https://github.com/e2bgateway/e2bgateway/issues/25) — [M1-R1.1][功能打平] E2B Cloud: ExecuteCodeStream 流式执行
**Date**: 2026-09-08
**Priority**: P0 — 阻塞交互式开发
**Approach**: A — 集成 WSProxy + streaming.Normalizer 实现流式输出标准化

---

## 1. Problem Statement

E2B Cloud adapter 的 `ExecuteCodeStream()` 返回 `streaming not yet implemented`，阻塞了交互式开发场景。作为 P0 功能打平任务，需要实现真正的流式代码执行，兼容 E2B Client SDK 协议。

## 2. Design Goals

- **协议打平**: 完全兼容 E2B Client SDK 协议，支持 ConnectRPC + WebSocket 双模式
- **流式输出**: 集成 WSProxy 实现流式代码执行输出标准化
- **帧标准化**: 使用 `streaming.Normalizer` 确保所有输出帧符合 E2B SDK 格式
- **降级策略**: WebSocket 不可用时自动降级为同步执行模式
- **上下文取消**: 支持 ctx 取消，正确清理 goroutine 和连接

## 3. Architecture

### 3.1 双模式协议兼容

```
E2B SDK Client
      │
      ├── ConnectRPC (数据面 — 已有)
      │   └── {port}-{sandboxID}.{sandboxDomain} → envd:49983
      │       → gateway catch-all proxy → httputil.ReverseProxy
      │       (支持 server-stream RPC，已工作)
      │
      └── WebSocket (新增)
          └── /sandboxes/{sandboxID}/ws → ExecuteCodeStreamHandler
              → adapter.ExecuteCodeStream()
              → CodeStreamer.Stream()
              → envd WebSocket
```

### 3.2 组件关系

```
internal/adapter/e2bcloud/
├── adapter.go       — Adapter.ExecuteCodeStream() 实现
│                     调用 CodeStreamer，WS 失败时 fallback
├── streamer.go      — CodeStreamer: 连接 envd WS，流式帧处理
│                     使用 streaming.Normalizer 标准化
├── websocket.go     — WSProxy: 已有，用于 gateway 级 WS 代理
└── client.go        — E2B Cloud HTTP 客户端

internal/api/v1/
└── ws_handler.go    — ExecuteCodeStreamHandler: WS handler
                        桥接 WS 帧 ↔ CodeStream 接口

internal/streaming/
├── frame.go         — Frame 类型和构造器
├── normalizer.go    — Normalizer: 输出帧标准化
└── ...              — Buffer, Relay, WSHandler 等
```

### 3.3 数据流

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

### 4.1 WebSocket 端点

```
GET /sandboxes/{sandboxID}/ws
Headers:
  X-Access-Token: <token>  (or ?access_token=<token>)

WebSocket frames:
  Client → Server:
    {"type":"code/exec","data":{"code":"...","language":"python"}}
    {"type":"cancel"}
    {"type":"keepAlive"}

  Server → Client:
    {"type":"stdout","data":{"content":"...","timestamp":"...","executionID":"..."}}
    {"type":"stderr","data":{"content":"...","timestamp":"...","executionID":"..."}}
    {"type":"result","data":{"exitCode":0,"duration":1.23}}
    {"type":"error","data":{"code":"runtime_error","message":"..."}}
    {"type":"keepAlive"}
```

### 4.2 Adapter 接口

无接口变更。`SandboxAdapter.ExecuteCodeStream()` 签名不变。

## 5. 配置

无新增配置项。

## 6. 安全考虑

- **Access Token 认证**: WS 连接前通过 `ValidateAccessToken` 验证
- **无 Shell 注入风险**: code 内容通过 WS 帧直接传递，不拼接 shell 命令
- **写超时**: WebSocket 写操作设置 30s 超时
- **上下文传播**: ctx 取消时关闭 WS 连接，终止 ReadMessage() 阻塞
- **帧大小**: gorilla/websocket 默认读缓冲区 4096 字节

## 7. 可观测性

- 结构化日志包含 `sandboxID`, `executionID`, `adapter`
- 执行时长通过 `result` 帧的 `duration` 字段暴露

## 8. 测试策略

| 层次 | 覆盖 |
|------|------|
| 单元测试 | CodeStreamer: WS 连接、帧处理、context 取消、连接拒绝 |
| 单元测试 | wsCodeStream: 帧标准化、Send/Close |
| Handler 测试 | WS 升级、code/exec 帧、缺失 sandboxID |
| 集成测试 | Adapter fallback (WS 不可用 → 同步) |
| E2E 测试 | 已有 envd proxy token enforcement 测试覆盖 |

## 9. 降级策略

```go
func (a *Adapter) ExecuteCodeStream(...) error {
    err := streamer.Stream(...)  // 尝试 WS 流式
    if err != nil && isWebSocketNotAvailable(err) {
        return a.streamFromSync(...)  // 降级为同步
    }
    return err
}
```

降级条件：`dial`, `connection refused`, `no such host`, `bad handshake`, `context canceled`

## 10. 文件清单

| 文件 | 操作 |
|------|------|
| `internal/adapter/e2bcloud/streamer.go` | 新建 |
| `internal/adapter/e2bcloud/streamer_test.go` | 新建 |
| `internal/adapter/e2bcloud/adapter.go` | 修改 |
| `internal/api/v1/ws_handler.go` | 新建 |
| `internal/api/v1/ws_handler_test.go` | 新建 |
| `internal/server/http.go` | 修改 |

## 11. 验收标准

- [x] `go test ./... -race` 全通过
- [x] `golangci-lint run` 零告警
- [x] `go vet ./...` 通过
- [x] ExecuteCodeStream 输出帧兼容 E2B SDK 协议
- [x] WS 不可用时自动降级为同步模式
- [x] Context 取消正确清理
