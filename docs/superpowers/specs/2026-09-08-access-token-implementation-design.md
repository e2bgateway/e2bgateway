# Access Token Implementation for Agent-Sandbox Adapter

**Issue**: [#23](https://github.com/e2bgateway/e2bgateway/issues/23) — [M1-R1.1][功能打平] Agent-Sandbox: Access Token 实现
**Date**: 2026-09-08
**Priority**: P0 — 阻塞 SDK ConnectRPC 代理转发
**Approach**: A — Per-adapter token cache + SandboxAdapter interface extension

---

## 1. Problem Statement

`GetAccessToken()` 在 agent-sandbox adapter 中返回 "not supported" 错误。E2B SDK 需要该接口来获取 scoped access token，用于直接连接 sandbox 内的 envd daemon。当前状态阻塞了 SDK ConnectRPC 代理转发功能。

## 2. Design Goals

- 实现 agent-sandbox adapter 的 `GetAccessToken()` 方法
- 生成 scoped token，包含 sandbox 绑定和 1h 有效期
- 服务端验证：envd proxy 转发前验证 token 有效性
- Token 重用：同一 sandbox 在未过期前返回相同 token
- 复用现有 `internal/cache` 基础设施

## 3. Architecture

### 3.1 接口扩展

`internal/adapter/interface.go` 新增方法:

```go
// ValidateAccessToken verifies a previously issued access token for a sandbox.
// Returns true if the token is valid and not expired.
ValidateAccessToken(ctx context.Context, sandboxID, token string) (bool, error)
```

### 3.2 agentsandbox adapter 实现

**新增字段**:

```go
type Adapter struct {
    // ... existing fields ...
    tokenCache cache.Cache  // token storage, key=sandboxID, value=token string
}
```

**初始化**:

`New()` 中创建 cache 实例，默认 TTL = 1h:

```go
tc := cache.New(10000, 1*time.Hour)  // maxSize=10000, TTL=1h
```

**GetAccessToken 流程**:

```
GetAccessToken(ctx, sandboxID)
  │
  ├─ resolveClaimName(sandboxID) → 验证 sandbox 存在
  │
  ├─ tokenCache.Get(sandboxID)
  │    ├─ 命中 → 返回已有 token + expiresAt
  │    └─ 未命中 ↓
  │
  ├─ 生成 token: "envd_{sandboxID}_{32位随机hex}"
  │
  ├─ tokenCache.Set(sandboxID, token, 1h TTL)
  │
  └─ 返回 AccessToken{Token, ExpiresAt}
```

**ValidateAccessToken 流程**:

```
ValidateAccessToken(ctx, sandboxID, token)
  │
  ├─ tokenCache.Get(sandboxID)
  │    ├─ 命中 → 比对 token，返回 true/false
  │    └─ 未命中 → 返回 false
  └─
```

### 3.3 其他 adapter 适配

| Adapter | GetAccessToken | ValidateAccessToken | 说明 |
|---|---|---|---|
| **mock** | 改用 cache 存储 + 重用 | 新增：比对 cache | 与 agentsandbox 一致 |
| **e2bcloud** | 保持不变（upstream） | 新增：返回 `(true, nil)` | upstream 已验证，gateway 无需重复验证 |
| **opensandbox** | 保持不变（not supported） | 新增：返回 not supported error | 保持现状 |
| **test stubs** | 已有 | 新增：返回 `(true, nil)` | 测试兼容 |

### 3.4 envd proxy 验证

`internal/server/envd_proxy.go` 修改 token 处理逻辑:

```go
// 当前: 直接转发 token
if token := r.Header.Get("X-Access-Token"); token != "" {
    req.Header.Set("Authorization", "Bearer "+token)
}

// 改为: 先验证再转发
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

req.Header.Set("Authorization", "Bearer "+token)
```

### 3.5 Token 格式

```
envd_{sandboxID}_{32位随机hex}
```

示例: `envd_abc123xyz_def456789012345678901234567890abcd`

设计考量:
- `envd_` 前缀：标识 token 类型，便于日志和调试
- `{sandboxID}`：token 与 sandbox 绑定，scoped 语义
- `{32位随机hex}`：128-bit 熵，不可预测

### 3.6 配置

无新增配置。Token cache 使用 in-memory LRU（`internal/cache` 包仅支持内存模式），固定参数:
- MaxSize: 10000 entries
- DefaultTTL: 1h

多副本部署时，同一 sandbox 的 token 可能在不同副本间不同。由于 SDK 创建 sandbox 后立即获取 token 并使用，且 envd proxy 路由到同一 adapter 实例（via routing），正常流量可工作。若需要强一致性跨副本验证，后续可扩展 cache 包支持 Redis。

## 4. Data Flow

```
E2B SDK
  │
  ├─ POST /sandboxes/{id}/access-token
  │    └─ GetAccessTokenHandler
  │         └─ adapter.GetAccessToken(sandboxID)
  │              ├─ cache hit → 返回已有 token
  │              └─ cache miss → 生成 + 存 cache → 返回
  │         └─ 响应: {"accessToken": "...", "expiresAt": "..."}
  │
  └─ ConnectRPC → envd (via envd proxy)
       └─ Header: X-Access-Token: ...
            └─ envdProxyHandler
                 ├─ 提取 sandboxID
                 ├─ 获取 adapter
                 ├─ adapter.ValidateAccessToken(sandboxID, token)
                 │    ├─ valid → 转发 req（Bearer token）
                 │    └─ invalid → 401
                 └─ httputil.ReverseProxy → envd daemon
```

## 5. Error Handling

| 场景 | 行为 |
|---|---|
| sandbox 不存在 | GetAccessToken 返回 error（resolveClaimName 失败） |
| cache 故障 | Get 失败 → 生成新 token 并尝试 Set；Set 失败 → 仍返回 token（降级：无服务端验证） |
| token 过期 | cache 自动清理，下次 GetAccessToken 生成新 token |
| ValidateAccessToken cache miss | 返回 false → 401 |
| envd proxy 无 token | 返回 401 |

## 6. Security Considerations

- Token 128-bit 随机，不可暴力破解
- 1h TTL 限制窗口期
- Scoped 到 sandboxID，一个 token 只能访问对应 sandbox
- envd proxy 强制验证，未携带或无效 token 返回 401
- Token 不存入 sandbox 或 K8s 资源，仅在 gateway 内存/Redis 中

## 7. Testing

- **单元测试** (`agentsandbox/adapter_test.go`):
  - TestGetAccessToken_GeneratesToken
  - TestGetAccessToken_ReusesExistingToken
  - TestGetAccessToken_SandboxNotFound
  - TestValidateAccessToken_Valid
  - TestValidateAccessToken_Invalid
  - TestValidateAccessToken_Expired
- **handler 测试** (`handlers_test.go`):
  - 更新 TestGetAccessTokenHandler
- **集成测试** (`test/integration/adapter_test.go`):
  - 现有并发测试应继续通过
- **envd proxy 测试**:
  - 验证 token 有效 → 转发
  - 验证 token 无效 → 401
  - 验证无 token → 401

## 8. Files to Modify

| File | Change |
|---|---|
| `internal/adapter/interface.go` | 新增 `ValidateAccessToken` 方法 |
| `internal/adapter/agentsandbox/adapter.go` | 新增 `tokenCache` 字段，实现 `GetAccessToken` + `ValidateAccessToken` |
| `internal/adapter/mock/adapter.go` | 改用 cache，实现 `ValidateAccessToken` |
| `internal/adapter/e2bcloud/adapter.go` | 新增 `ValidateAccessToken` 返回 `true` |
| `internal/adapter/opensandbox/adapter.go` | 新增 `ValidateAccessToken` 返回 not supported |
| `internal/server/envd_proxy.go` | 新增 token 验证逻辑 |
| `test/mock/backend.go` | 新增 `ValidateAccessToken` mock |
| `internal/routing/router_test.go` | stub adapter 新增 `ValidateAccessToken` |
| `internal/adapter/agentsandbox/adapter_test.go` | 新增 token 相关测试 |

## 9. Migration

无破坏性变更。新接口方法对所有 adapter 增加，现有调用方（GetAccessTokenHandler）不受影响。envd proxy 新增验证为向后兼容：之前无 token 也能通过，现在有 token 才通过。但由于 E2B SDK 始终会发送 token，对正常用户无影响。

## 10. Out of Scope

- opensandbox adapter 的 GetAccessToken 实现（后续 issue）
- 分布式 token cache（当前 in-memory，后续可切换 Redis）
- Token 刷新/撤销 API
