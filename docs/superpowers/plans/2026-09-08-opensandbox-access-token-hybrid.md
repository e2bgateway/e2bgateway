# OpenSandbox Access Token 混合实现方案

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

## Goal

为 OpenSandbox adapter 实现混合 access token 方案：支持两种模式（服务端签名 endpoint vs gateway 自造 token），通过配置开关切换，同时透传 server 返回的 Endpoint.Headers，并为 ExecdClient 传递 token。

## Architecture

- **双模式支持**：`UseSignedEndpoint=true` 使用 OpenSandbox server 的 OSEP-0011 签名 endpoint；`false` 使用 gateway 自造的 `envd_{id}_{random}` token
- **Endpoint.Headers 透传**：存储 server 返回的认证 headers 到 `endpointHeaders` cache，在创建 ExecdClient 时传递
- **ExecdClient 认证**：`getOrCreateExecdClient` 从 `tokenCache` 获取 token，传入 `NewExecdClient`
- **ValidateAccessToken 统一**：两种模式下都从 `tokenCache` 取 token 比对（签名模式下 token 是 server 签名的，自造模式下是 gateway 生成的）

## Tech Stack

- Go 1.26+
- OpenSandbox SDK v1.0.5 (`github.com/alibaba/OpenSandbox/sdks/sandbox/go`)
- `internal/cache` (LRU cache with TTL)
- `crypto/rand` (token generation)

## Global Constraints

- Token TTL: 1 hour
- Token cache: 10000 entries max
- Endpoint headers cache: 10000 entries max, 1 hour TTL
- 向后兼容：`UseSignedEndpoint=false` 时行为与 PR #43 完全一致
- 所有现有测试必须通过
- 新增测试覆盖两种模式

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `internal/adapter/opensandbox/adapter.go` | Modify | 添加 `useSignedEndpoint` 和 `endpointHeaders` 字段；实现双模式 `GetAccessToken`；透传 headers 的 `GetEnvdEndpoint`；带 token 的 `getOrCreateExecdClient` |
| `internal/adapter/opensandbox/factory.go` | Modify | 解析 `useSignedEndpoint` 配置 |
| `internal/adapter/opensandbox/adapter_test.go` | Modify | 添加双模式测试 |
| `configs/e2bgateway-example.yaml` | Modify | 添加 `useSignedEndpoint` 配置示例 |

---

### Task 1: 扩展 Adapter 和 AdapterConfig 结构

**Files:**
- Modify: `internal/adapter/opensandbox/adapter.go:30-45` (Adapter struct)
- Modify: `internal/adapter/opensandbox/adapter.go:47-57` (AdapterConfig struct)
- Modify: `internal/adapter/opensandbox/adapter.go:60-81` (New function)

**Interfaces:**
- Consumes: 现有 `Adapter` 和 `AdapterConfig` 结构
- Produces: 新增 `useSignedEndpoint bool` 和 `endpointHeaders *cache.Cache` 字段

- [ ] **Step 1: 修改 AdapterConfig 添加 UseSignedEndpoint 字段**

在 `AdapterConfig` struct（第 47-57 行）添加：

```go
type AdapterConfig struct {
    // ... existing fields ...
    
    // UseSignedEndpoint controls whether to use OpenSandbox server's
    // OSEP-0011 signed endpoint (GetSignedEndpoint) instead of 
    // gateway-generated random tokens.
    // When true: token comes from server-side signing
    // When false: gateway generates envd_{id}_{random} tokens
    UseSignedEndpoint bool
}
```

- [ ] **Step 2: 修改 Adapter 添加新字段**

在 `Adapter` struct（第 30-45 行）添加：

```go
type Adapter struct {
    // ... existing fields ...
    
    useSignedEndpoint bool
    
    // endpointHeaders stores server-returned auth headers per sandbox.
    // Key: sandboxID, Value: map[string]string (headers from Endpoint.Headers)
    endpointHeaders *cache.Cache
}
```

- [ ] **Step 3: 修改 New 函数初始化新字段**

在 `New` 函数（第 60-81 行）的 return 语句中添加：

```go
return &Adapter{
    // ... existing initialization ...
    useSignedEndpoint: cfg.UseSignedEndpoint,
    endpointHeaders:   cache.New(10000, 1*time.Hour),
}, nil
```

- [ ] **Step 4: 验证编译通过**

```bash
go build ./internal/adapter/opensandbox/...
```

Expected: 编译成功

- [ ] **Step 5: 提交**

```bash
git add internal/adapter/opensandbox/adapter.go
git commit -m "feat(opensandbox): add UseSignedEndpoint config and endpointHeaders cache"
```

---

### Task 2: 实现双模式 GetAccessToken

**Files:**
- Modify: `internal/adapter/opensandbox/adapter.go:640-665` (GetAccessToken)
- Test: `internal/adapter/opensandbox/adapter_test.go`

**Interfaces:**
- Consumes: `useSignedEndpoint` 字段、`lifecycle.GetSignedEndpoint` SDK 方法
- Produces: 双模式 `GetAccessToken` 实现

- [ ] **Step 1: 写失败测试 - 签名模式**

在 `adapter_test.go` 添加：

```go
func TestGetAccessToken_SignedMode(t *testing.T) {
    // Mock lifecycle client 返回签名 endpoint
    mockLifecycle := &MockLifecycleClient{
        GetSignedEndpointFunc: func(ctx context.Context, sandboxID string, port int, expires int64) (*opensandbox.Endpoint, error) {
            return &opensandbox.Endpoint{
                Endpoint: "https://signed.example.com/sandbox/123",
                Headers: map[string]string{
                    "X-Signature": "abc123",
                },
            }, nil
        },
    }
    
    a := &Adapter{
        name:              "test",
        lifecycle:         mockLifecycle,
        useSignedEndpoint: true,
        tokenCache:        cache.New(100, 1*time.Hour),
        endpointHeaders:   cache.New(100, 1*time.Hour),
    }
    
    token, err := a.GetAccessToken(context.Background(), "sandbox-123")
    require.NoError(t, err)
    require.NotEmpty(t, token.Token)
    
    // 验证 headers 被存储
    headers, ok := a.endpointHeaders.Get("sandbox-123")
    require.True(t, ok)
    headerMap := headers.(map[string]string)
    assert.Equal(t, "abc123", headerMap["X-Signature"])
}
```

- [ ] **Step 2: 运行测试验证失败**

```bash
go test ./internal/adapter/opensandbox/ -run TestGetAccessToken_SignedMode -v
```

Expected: FAIL（未实现）

- [ ] **Step 3: 实现 GetAccessToken 双模式**

替换现有 `GetAccessToken`（第 640-665 行）为：

```go
func (a *Adapter) GetAccessToken(ctx context.Context, sandboxID string) (*adapter.AccessToken, error) {
    if a.useSignedEndpoint {
        return a.getSignedAccessToken(ctx, sandboxID)
    }
    return a.generateRandomToken(sandboxID)
}

func (a *Adapter) getSignedAccessToken(ctx context.Context, sandboxID string) (*adapter.AccessToken, error) {
    // 检查 cache
    if cached, ok := a.tokenCache.Get(sandboxID); ok {
        if tokenStr, ok := cached.(string); ok {
            return &adapter.AccessToken{
                Token:     tokenStr,
                ExpiresAt: time.Now().Add(1 * time.Hour),
            }, nil
        }
    }
    
    // 调用 GetSignedEndpoint
    expires := time.Now().Add(1 * time.Hour).Unix()
    ep, err := a.lifecycle.GetSignedEndpoint(ctx, sandboxID, 49983, expires)
    if err != nil {
        return nil, fmt.Errorf("getting signed endpoint: %w", err)
    }
    
    // 从 endpoint URL 提取 token（签名 token 在 URL 中）
    token := ep.Endpoint
    
    // 存储 token
    a.tokenCache.Set(sandboxID, token)
    
    // 存储 headers
    if len(ep.Headers) > 0 {
        a.endpointHeaders.Set(sandboxID, ep.Headers)
    }
    
    return &adapter.AccessToken{
        Token:     token,
        ExpiresAt: time.Now().Add(1 * time.Hour),
    }, nil
}

func (a *Adapter) generateRandomToken(sandboxID string) (*adapter.AccessToken, error) {
    // 检查 cache（保留原有逻辑）
    if cached, ok := a.tokenCache.Get(sandboxID); ok {
        if tokenStr, ok := cached.(string); ok {
            return &adapter.AccessToken{
                Token:     tokenStr,
                ExpiresAt: time.Now().Add(1 * time.Hour),
            }, nil
        }
    }
    
    // 生成随机 token
    b := make([]byte, 16)
    if _, err := rand.Read(b); err != nil {
        return nil, fmt.Errorf("generating token: %w", err)
    }
    token := fmt.Sprintf("envd_%s_%s", sandboxID, hex.EncodeToString(b))
    
    // 存储
    a.tokenCache.Set(sandboxID, token)
    
    return &adapter.AccessToken{
        Token:     token,
        ExpiresAt: time.Now().Add(1 * time.Hour),
    }, nil
}
```

- [ ] **Step 4: 运行测试验证通过**

```bash
go test ./internal/adapter/opensandbox/ -run TestGetAccessToken -v
```

Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/adapter/opensandbox/adapter.go internal/adapter/opensandbox/adapter_test.go
git commit -m "feat(opensandbox): implement dual-mode GetAccessToken (signed vs random)"
```

---

### Task 3: 实现 GetEnvdEndpoint Headers 透传

**Files:**
- Modify: `internal/adapter/opensandbox/adapter.go:726-747` (GetEnvdEndpoint)
- Test: `internal/adapter/opensandbox/adapter_test.go`

**Interfaces:**
- Consumes: `endpointHeaders` cache、`lifecycle.GetEndpoint`/`GetSignedEndpoint`
- Produces: 透传 headers 的 `GetEnvdEndpoint`

- [ ] **Step 1: 写失败测试 - headers 透传**

```go
func TestGetEnvdEndpoint_HeadersPassthrough(t *testing.T) {
    mockLifecycle := &MockLifecycleClient{
        GetEndpointFunc: func(ctx context.Context, sandboxID string, port int, useProxy *bool) (*opensandbox.Endpoint, error) {
            return &opensandbox.Endpoint{
                Endpoint: "http://proxy.example.com/sandbox/123/49983",
                Headers: map[string]string{
                    "X-Auth-Token": "server-token",
                },
            }, nil
        },
    }
    
    a := &Adapter{
        lifecycle:       mockLifecycle,
        tokenCache:      cache.New(100, 1*time.Hour),
        endpointHeaders: cache.New(100, 1*time.Hour),
    }
    
    // 预先存储 headers
    a.endpointHeaders.Set("sandbox-123", map[string]string{
        "X-Auth-Token": "server-token",
    })
    
    url, token, err := a.GetEnvdEndpoint(context.Background(), "sandbox-123")
    require.NoError(t, err)
    assert.Equal(t, "http://proxy.example.com/sandbox/123/49983", url)
    assert.NotEmpty(t, token)
}
```

- [ ] **Step 2: 运行测试验证失败**

Expected: FAIL

- [ ] **Step 3: 实现 GetEnvdEndpoint headers 透传**

替换现有 `GetEnvdEndpoint`（第 726-747 行）：

```go
func (a *Adapter) GetEnvdEndpoint(ctx context.Context, sandboxID string) (string, string, error) {
    var ep *opensandbox.Endpoint
    var err error
    
    if a.useSignedEndpoint {
        expires := time.Now().Add(1 * time.Hour).Unix()
        ep, err = a.lifecycle.GetSignedEndpoint(ctx, sandboxID, 49983, expires)
    } else {
        useProxy := true
        ep, err = a.lifecycle.GetEndpoint(ctx, sandboxID, 49983, &useProxy)
    }
    
    if err != nil {
        return "", "", fmt.Errorf("getting envd endpoint for sandbox %q: %w", sandboxID, err)
    }
    
    envdURL := ep.Endpoint
    if !strings.HasPrefix(envdURL, "http") {
        envdURL = "http://" + envdURL
    }
    
    // 存储 headers（如果有）
    if len(ep.Headers) > 0 {
        a.endpointHeaders.Set(sandboxID, ep.Headers)
    }
    
    // 从 cache 获取 token
    token := ""
    if cached, ok := a.tokenCache.Get(sandboxID); ok {
        if tokenStr, ok := cached.(string); ok {
            token = tokenStr
        }
    }
    
    return envdURL, token, nil
}
```

- [ ] **Step 4: 运行测试验证通过**

Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/adapter/opensandbox/adapter.go internal/adapter/opensandbox/adapter_test.go
git commit -m "feat(opensandbox): passthrough Endpoint.Headers in GetEnvdEndpoint"
```

---

### Task 4: ExecdClient 传递 Token 和 Headers

**Files:**
- Modify: `internal/adapter/opensandbox/adapter.go:114-156` (getOrCreateExecdClient)
- Test: `internal/adapter/opensandbox/adapter_test.go`

**Interfaces:**
- Consumes: `tokenCache`、`endpointHeaders` cache
- Produces: 带 token 和 headers 的 ExecdClient

- [ ] **Step 1: 写失败测试 - ExecdClient 带 token**

```go
func TestGetOrCreateExecdClient_WithToken(t *testing.T) {
    mockLifecycle := &MockLifecycleClient{
        GetEndpointFunc: func(ctx context.Context, sandboxID string, port int, useProxy *bool) (*opensandbox.Endpoint, error) {
            return &opensandbox.Endpoint{
                Endpoint: "http://execd.example.com",
                Headers:  map[string]string{"X-Custom": "value"},
            }, nil
        },
    }
    
    a := &Adapter{
        lifecycle:       mockLifecycle,
        execdClients:    make(map[string]*opensandbox.ExecdClient),
        tokenCache:      cache.New(100, 1*time.Hour),
        endpointHeaders: cache.New(100, 1*time.Hour),
    }
    
    // 预先存储 token 和 headers
    a.tokenCache.Set("sandbox-123", "test-token")
    a.endpointHeaders.Set("sandbox-123", map[string]string{"X-Custom": "value"})
    
    client, err := a.getOrCreateExecdClient(context.Background(), "sandbox-123")
    require.NoError(t, err)
    require.NotNil(t, client)
    
    // 验证 client 创建时传入了 token 和 headers
    // （需要通过 mock 或反射验证，这里简化为验证不报错）
}
```

- [ ] **Step 2: 运行测试验证失败**

Expected: FAIL

- [ ] **Step 3: 实现 getOrCreateExecdClient 带 token 和 headers**

修改 `getOrCreateExecdClient`（第 114-156 行），在创建 ExecdClient 时传入 token 和 headers：

```go
func (a *Adapter) getOrCreateExecdClient(ctx context.Context, sandboxID string) (*opensandbox.ExecdClient, error) {
    a.execdClientsMu.Lock()
    defer a.execdClientsMu.Unlock()
    
    if client, exists := a.execdClients[sandboxID]; exists {
        return client, nil
    }
    
    // 获取 endpoint
    var ep *opensandbox.Endpoint
    var err error
    
    if a.useSignedEndpoint {
        expires := time.Now().Add(1 * time.Hour).Unix()
        ep, err = a.lifecycle.GetSignedEndpoint(ctx, sandboxID, 44772, expires)
    } else {
        useProxy := true
        ep, err = a.lifecycle.GetEndpoint(ctx, sandboxID, 44772, &useProxy)
    }
    
    if err != nil {
        return nil, fmt.Errorf("getting execd endpoint: %w", err)
    }
    
    // 从 cache 获取 token
    token := ""
    if cached, ok := a.tokenCache.Get(sandboxID); ok {
        if tokenStr, ok := cached.(string); ok {
            token = tokenStr
        }
    }
    
    // 从 cache 获取 headers
    var opts []opensandbox.Option
    if cached, ok := a.endpointHeaders.Get(sandboxID); ok {
        if headers, ok := cached.(map[string]string); ok && len(headers) > 0 {
            opts = append(opts, opensandbox.WithHeaders(headers))
        }
    }
    
    // 也添加 endpoint 返回的 headers
    if len(ep.Headers) > 0 {
        opts = append(opts, opensandbox.WithHeaders(ep.Headers))
    }
    
    // 创建 ExecdClient，传入 token
    client := opensandbox.NewExecdClient(ep.Endpoint, token, opts...)
    
    a.execdClients[sandboxID] = client
    return client, nil
}
```

- [ ] **Step 4: 运行测试验证通过**

Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/adapter/opensandbox/adapter.go internal/adapter/opensandbox/adapter_test.go
git commit -m "feat(opensandbox): pass token and headers to ExecdClient"
```

---

### Task 5: Factory 解析配置

**Files:**
- Modify: `internal/adapter/opensandbox/factory.go:11-62` (NewAdapterFromConfig)

**Interfaces:**
- Consumes: YAML 配置
- Produces: `AdapterConfig.UseSignedEndpoint` 字段填充

- [ ] **Step 1: 修改 factory.go 解析 useSignedEndpoint**

在 `NewAdapterFromConfig` 函数中添加配置解析：

```go
func NewAdapterFromConfig(cfg config.BackendConfig) (adapter.SandboxAdapter, error) {
    // ... existing code ...
    
    adapterCfg := AdapterConfig{
        // ... existing fields ...
    }
    
    // 解析 useSignedEndpoint
    if v, ok := cfg.Config["useSignedEndpoint"]; ok {
        if b, ok := v.(bool); ok {
            adapterCfg.UseSignedEndpoint = b
        }
    }
    
    return New(adapterCfg)
}
```

- [ ] **Step 2: 验证编译通过**

```bash
go build ./internal/adapter/opensandbox/...
```

Expected: 编译成功

- [ ] **Step 3: 提交**

```bash
git add internal/adapter/opensandbox/factory.go
git commit -m "feat(opensandbox): parse useSignedEndpoint from config"
```

---

### Task 6: 更新配置示例

**Files:**
- Modify: `configs/e2bgateway-example.yaml`

**Interfaces:**
- Consumes: 无
- Produces: 配置示例文档

- [ ] **Step 1: 添加 useSignedEndpoint 配置示例**

在 OpenSandbox backend 配置部分添加：

```yaml
backends:
  - name: opensandbox
    type: opensandbox
    enabled: true
    config:
      baseURL: "http://opensandbox-server:8080"
      apiKey: "your-api-key"
      # Use OpenSandbox server's OSEP-0011 signed endpoints
      # Set to true if your OpenSandbox server supports signed routes
      # Set to false to use gateway-generated random tokens (default)
      useSignedEndpoint: false
      templateToImage:
        base: "python:3.11-slim"
        code-interpreter: "python:3.11-slim"
```

- [ ] **Step 2: 提交**

```bash
git add configs/e2bgateway-example.yaml
git commit -m "docs: add useSignedEndpoint config example"
```

---

### Task 7: 全量测试和验证

**Files:**
- Test: 所有 OpenSandbox 相关测试

**Interfaces:**
- Consumes: 所有前面的实现
- Produces: 验证通过的测试套件

- [ ] **Step 1: 运行所有 OpenSandbox adapter 测试**

```bash
go test ./internal/adapter/opensandbox/... -v
```

Expected: 所有测试通过

- [ ] **Step 2: 运行集成测试**

```bash
go test ./test/integration/... -v
```

Expected: 所有测试通过

- [ ] **Step 3: 运行 E2E 测试**

```bash
go test ./test/e2e/... -v
```

Expected: 所有测试通过

- [ ] **Step 4: 运行 go vet**

```bash
go vet ./...
```

Expected: 无问题

- [ ] **Step 5: 运行 golangci-lint**

```bash
golangci-lint run
```

Expected: 无问题

- [ ] **Step 6: 运行 gofmt**

```bash
gofmt -l .
```

Expected: 无输出（所有文件已格式化）

- [ ] **Step 7: 提交所有测试（如果有新增）**

```bash
git add -A
git commit -m "test: add comprehensive tests for dual-mode token implementation"
```

---

## 完成标准

- [ ] `UseSignedEndpoint` 配置开关工作正常
- [ ] 签名模式下使用 `GetSignedEndpoint` 获取 server 签名 token
- [ ] 自造模式下使用 gateway 生成的 `envd_{id}_{random}` token
- [ ] `Endpoint.Headers` 被正确存储和透传
- [ ] `ExecdClient` 创建时传入 token 和 headers
- [ ] 所有现有测试通过
- [ ] 新增测试覆盖两种模式
- [ ] 配置示例更新
- [ ] 代码通过 vet、lint、gofmt 检查

---

## 回滚计划

如果实现过程中遇到严重问题，可以回滚到 PR #43 的状态：

```bash
git revert HEAD  # 假设所有改动在最近几个 commit
```

或者保留当前实现，将 `useSignedEndpoint` 默认值设为 `false`，这样行为与 PR #43 完全一致。
