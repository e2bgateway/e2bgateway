# Warm Pool CRUD (Agent-Sandbox + OpenSandbox) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 agentsandbox 与 opensandbox 两个 adapter 的 Warm Pool CRUD 5 个 stub 方法实现为真实功能（issue #32）。

**Architecture:** agentsandbox 映射到 K8s `SandboxWarmPool` CRD（经 `K8sHelper.ExtensionsClient` typed clientset，单测用 SDK 自带 fake）；opensandbox 用 SDK v1.2.0 客户端池（`DefaultSandboxPool`）建进程内注册表，`CreateSandbox` 自动优先 `Acquire`、失败回落直连。HTTP 层 / DTO / `SandboxAdapter` 接口均不改。

**Tech Stack:** Go 1.26；`sigs.k8s.io/agent-sandbox v1.0.10`（typed clientset + fake）；`github.com/alibaba/OpenSandbox/sdks/sandbox/go v1.2.0`（`SandboxPool`、`poolredis`）。

**Spec:** [2026-11-12-warm-pool-crud-design.md](../specs/2026-11-12-warm-pool-crud-design.md)
**Issue:** #32
**Branch:** `add-warmpool`（已存在，直接在此分支提交）

## Global Constraints

- 不改 `internal/adapter/interface.go` 的 `SandboxAdapter` 接口签名；不改 `internal/api/v1/handlers.go`、`internal/server/http.go`、`internal/api/dto/`
- e2bcloud / mock adapter 不动（已实现），但每次提交后全仓 `go build ./...` 必须通过
- `TargetSize` 上限默认 **256**（`DefaultMaxWarmPoolSize`），`maxWarmPoolSize` 可配；负数 → 报错
- WarmPoolID 规范：`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`，≤63 字符（K8s name 兼容，两后端统一）
- Status 统一取值：`pending` / `scaling` / `ready` / `degraded` / `destroyed`
- Viper 小写化：所有新配置 key 解析必须同时查 `camelCase` 与全小写变体
- 每个 Task 结束跑 `go test -race ./internal/adapter/...` 并提交；最终 `make pre-commit` 通过
- 提交信息遵循仓库惯例（conventional commits，参考 `git log`：`feat(scope): ...`）

---

### Task 1: 共用基础 — 哨兵错误 + ID 校验/生成 + Size clamp

**Files:**
- Create: `internal/adapter/warmpool.go`
- Test: `internal/adapter/warmpool_test.go`

**Interfaces:**
- Consumes: 无（纯新增）
- Produces（Task 2-6 依赖，签名必须一致）:
  - `var ErrWarmPoolNotFound = errors.New("warm pool not found")`
  - `var ErrWarmPoolAlreadyExists = errors.New("warm pool already exists")`
  - `const DefaultMaxWarmPoolSize = 256`
  - `func ValidateWarmPoolID(id string) error`
  - `func GenerateWarmPoolID(templateID string) string`
  - `func ClampTargetSize(n, max int) (int, error)` — n<0 返回错误；n>max 返回 max；max<=0 时用 `DefaultMaxWarmPoolSize`

- [ ] **Step 1: 写失败测试** `internal/adapter/warmpool_test.go`

```go
package adapter

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateWarmPoolID(t *testing.T) {
	valid := []string{"a", "wp-base-1a2b3c4d", "pool-0", "x" + strings.Repeat("-y", 31)}
	for _, id := range valid {
		if err := ValidateWarmPoolID(id); err != nil {
			t.Errorf("ValidateWarmPoolID(%q) = %v, want nil", id, err)
		}
	}
	invalid := []string{
		"",                                   // 空
		"-lead",                              // 前导 -
		"trail-",                             // 尾随 -
		"Upper",                              // 大写
		"with space",                         // 空格
		"brace{id}",                          // Redis key 注入字符
		strings.Repeat("a", 64),              // 超长
	}
	for _, id := range invalid {
		if err := ValidateWarmPoolID(id); err == nil {
			t.Errorf("ValidateWarmPoolID(%q) = nil, want error", id)
		}
	}
}

func TestGenerateWarmPoolID(t *testing.T) {
	id := GenerateWarmPoolID("My_Template!v2")
	if err := ValidateWarmPoolID(id); err != nil {
		t.Fatalf("GenerateWarmPoolID produced invalid ID %q: %v", id, err)
	}
	if !strings.HasPrefix(id, "wp-") {
		t.Errorf("GenerateWarmPoolID(%q) = %q, want prefix wp-", "My_Template!v2", id)
	}
	// 确定性：同输入同输出
	if id != GenerateWarmPoolID("My_Template!v2") {
		t.Error("GenerateWarmPoolID is not deterministic")
	}
	// 不同输入不同输出
	if id == GenerateWarmPoolID("other-template") {
		t.Error("GenerateWarmPoolID collided across different templates")
	}
	// 长 templateID 不超长
	if long := GenerateWarmPoolID(strings.Repeat("t", 200)); len(long) > 63 {
		t.Errorf("GenerateWarmPoolID(long) len = %d, want <= 63", len(long))
	}
}

func TestClampTargetSize(t *testing.T) {
	tests := []struct {
		name    string
		n, max  int
		want    int
		wantErr bool
	}{
		{"normal", 5, 256, 5, false},
		{"zero", 0, 256, 0, false},
		{"at max", 256, 256, 256, false},
		{"above max clamps", 1000, 256, 256, false},
		{"negative errors", -1, 256, 0, true},
		{"non-positive max uses default", 300, 0, 256, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ClampTargetSize(tt.n, tt.max)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ClampTargetSize(%d, %d) err = %v, wantErr %v", tt.n, tt.max, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ClampTargetSize(%d, %d) = %d, want %d", tt.n, tt.max, got, tt.want)
			}
		})
	}
}

func TestSentinelErrorsDistinct(t *testing.T) {
	if errors.Is(ErrWarmPoolNotFound, ErrWarmPoolAlreadyExists) {
		t.Error("sentinel errors must be distinct")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/adapter/ -run 'TestValidateWarmPoolID|TestGenerateWarmPoolID|TestClampTargetSize|TestSentinelErrorsDistinct' -v`
Expected: 编译失败（`undefined: ValidateWarmPoolID` 等）

- [ ] **Step 3: 最小实现** `internal/adapter/warmpool.go`

```go
// Copyright The E2BGateway Authors
// (…Apache 2.0 header，与仓库其他文件一致…)

package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Sentinel errors for warm pool CRUD. Handlers currently swallow per-adapter
// errors (first-success-wins); these exist so adapters share consistent
// semantics and tests can assert with errors.Is.
var (
	ErrWarmPoolNotFound      = errors.New("warm pool not found")
	ErrWarmPoolAlreadyExists = errors.New("warm pool already exists")
)

// DefaultMaxWarmPoolSize caps TargetSize to prevent resource exhaustion (DoS).
const DefaultMaxWarmPoolSize = 256

// warmPoolIDPattern is K8s-name compatible so the same ID works as a
// SandboxWarmPool metadata.name and as an OpenSandbox pool/Redis key.
var warmPoolIDPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

const warmPoolIDMaxLen = 63

// ValidateWarmPoolID returns an error if id is not a valid warm pool ID.
func ValidateWarmPoolID(id string) error {
	if len(id) > warmPoolIDMaxLen || !warmPoolIDPattern.MatchString(id) {
		return fmt.Errorf("invalid warm pool ID %q: must match %s and be at most %d characters",
			id, warmPoolIDPattern, warmPoolIDMaxLen)
	}
	return nil
}

// GenerateWarmPoolID derives a deterministic, valid ID: wp-{slug}-{hash8}.
func GenerateWarmPoolID(templateID string) string {
	sum := sha256.Sum256([]byte(templateID))
	hash := hex.EncodeToString(sum[:])[:8]

	// Slug: lowercase, invalid chars → "-", trim, cap length so total ≤ 63.
	slug := strings.ToLower(templateID)
	slug = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if max := warmPoolIDMaxLen - len("wp--") - len(hash); len(slug) > max {
		slug = strings.TrimRight(slug[:max], "-")
	}
	if slug == "" {
		slug = "pool"
	}
	return "wp-" + slug + "-" + hash
}

// ClampTargetSize validates n ≥ 0 and caps it at max. max ≤ 0 falls back to
// DefaultMaxWarmPoolSize.
func ClampTargetSize(n, max int) (int, error) {
	if n < 0 {
		return 0, fmt.Errorf("targetSize must be >= 0, got %d", n)
	}
	if max <= 0 {
		max = DefaultMaxWarmPoolSize
	}
	if n > max {
		return max, nil
	}
	return n, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test -race ./internal/adapter/ -run 'WarmPool|Clamp|Sentinel' -v`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add internal/adapter/warmpool.go internal/adapter/warmpool_test.go
git commit -m "feat(adapter): add shared warm pool ID validation, generation and size clamping"
```

---

### Task 2: agentsandbox — 配置扩展 + CreateWarmPool / GetWarmPool

**Files:**
- Create: `internal/adapter/agentsandbox/warmpool.go`
- Modify: `internal/adapter/agentsandbox/adapter.go`（`AdapterConfig` 结构体、`New()` 内默认值装配、删除 `adapter.go:1179-1185` 的 Create/Get stub）
- Modify: `internal/adapter/agentsandbox/factory.go:42-55`（`parseBackendConfig` 新增解析）
- Test: `internal/adapter/agentsandbox/warmpool_test.go`

**Interfaces:**
- Consumes: Task 1 的 `adapter.ErrWarmPoolNotFound/AlreadyExists`、`ValidateWarmPoolID`、`GenerateWarmPoolID`、`ClampTargetSize`、`DefaultMaxWarmPoolSize`
- Produces（Task 3 依赖）:
  - `Adapter` 新字段：`maxWarmPoolSize int`、`warmPoolUpdateStrategy string`、`templateToSandboxTemplate map[string]string`
  - `func (a *Adapter) resolveTemplateRef(templateID string) string`
  - `func (a *Adapter) toAdapterWarmPool(swp *extv1beta1.SandboxWarmPool) *adapter.WarmPool`
  - `func warmPoolStatus(swp *extv1beta1.SandboxWarmPool) string`
  - 测试辅助：`func newWarmPoolTestAdapter(t *testing.T, objs ...runtime.Object) (*Adapter, *extfake.Clientset)`

SDK 事实（已验证）：`a.k8s.ExtensionsClient` 类型为 `extensionsv1beta1.ExtensionsV1beta1Interface`，含 `SandboxWarmPools(ns)` 与 `SandboxTemplates(ns)`；fake 包 `sigs.k8s.io/agent-sandbox/clients/k8s/extensions/clientset/versioned/fake` 提供 `NewSimpleClientset(objs ...runtime.Object)`。CRD 类型在 `sigs.k8s.io/agent-sandbox/extensions/api/v1beta1`（`SandboxWarmPoolSpec.Replicas *int32`、`TemplateRef.Name`、`Status.ReadyReplicas/ObservedGeneration`）。

- [ ] **Step 1: 写失败测试** `internal/adapter/agentsandbox/warmpool_test.go`

```go
package agentsandbox

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	extfake "sigs.k8s.io/agent-sandbox/clients/k8s/extensions/clientset/versioned/fake"
	"sigs.k8s.io/agent-sandbox/clients/go/sandbox"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
)

func newWarmPoolTestAdapter(t *testing.T, objs ...runtime.Object) (*Adapter, *extfake.Clientset) {
	t.Helper()
	cs := extfake.NewSimpleClientset(objs...)
	a := &Adapter{
		name:                      "agentsandbox",
		namespace:                 "default",
		k8s:                       &sandbox.K8sHelper{ExtensionsClient: cs.ExtensionsV1beta1()},
		warmPoolMap:               make(map[string]string),
		templateToSandboxTemplate: make(map[string]string),
		maxWarmPoolSize:           256,
	}
	return a, cs
}

func sandboxTemplate(name string) *extv1beta1.SandboxTemplate {
	return &extv1beta1.SandboxTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
	}
}

func TestCreateWarmPool_Success(t *testing.T) {
	a, cs := newWarmPoolTestAdapter(t, sandboxTemplate("base"))
	wp, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 3, WarmPoolID: "wp-base-test",
	})
	if err != nil {
		t.Fatalf("CreateWarmPool() error = %v", err)
	}
	if wp.WarmPoolID != "wp-base-test" || wp.TemplateID != "base" || wp.TargetSize != 3 {
		t.Errorf("CreateWarmPool() = %+v, want id=wp-base-test template=base size=3", wp)
	}
	// CRD 实际写入且字段正确
	got, err := cs.ExtensionsV1beta1().SandboxWarmPools("default").
		Get(context.Background(), "wp-base-test", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("CRD not created: %v", err)
	}
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 3 {
		t.Errorf("spec.replicas = %v, want 3", got.Spec.Replicas)
	}
	if got.Spec.TemplateRef.Name != "base" {
		t.Errorf("spec.sandboxTemplateRef.name = %q, want base", got.Spec.TemplateRef.Name)
	}
	// 运行时 warmPoolMap 回写，新池立即可被 CreateSandbox 消费
	if a.warmPoolMap["base"] != "wp-base-test" {
		t.Errorf("warmPoolMap[base] = %q, want wp-base-test", a.warmPoolMap["base"])
	}
}

func TestCreateWarmPool_GeneratesValidID(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t, sandboxTemplate("base"))
	wp, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1,
	})
	if err != nil {
		t.Fatalf("CreateWarmPool() error = %v", err)
	}
	if err := adapter.ValidateWarmPoolID(wp.WarmPoolID); err != nil {
		t.Errorf("generated ID invalid: %v", err)
	}
}

func TestCreateWarmPool_TemplateMissing(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t) // 无 SandboxTemplate
	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "ghost", TargetSize: 1,
	})
	if err == nil {
		t.Fatal("CreateWarmPool() = nil error, want template-not-found error")
	}
}

func TestCreateWarmPool_Duplicate(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t, sandboxTemplate("base"))
	req := &adapter.WarmPoolCreateRequest{TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-dup"}
	if _, err := a.CreateWarmPool(context.Background(), req); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := a.CreateWarmPool(context.Background(), req)
	if !errors.Is(err, adapter.ErrWarmPoolAlreadyExists) {
		t.Errorf("second create err = %v, want ErrWarmPoolAlreadyExists", err)
	}
}

func TestCreateWarmPool_ClampsSize(t *testing.T) {
	a, cs := newWarmPoolTestAdapter(t, sandboxTemplate("base"))
	wp, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 100000, WarmPoolID: "wp-clamp",
	})
	if err != nil {
		t.Fatalf("CreateWarmPool() error = %v", err)
	}
	if wp.TargetSize != 256 {
		t.Errorf("TargetSize = %d, want 256", wp.TargetSize)
	}
	got, _ := cs.ExtensionsV1beta1().SandboxWarmPools("default").
		Get(context.Background(), "wp-clamp", metav1.GetOptions{})
	if *got.Spec.Replicas != 256 {
		t.Errorf("spec.replicas = %d, want 256", *got.Spec.Replicas)
	}
}

func TestCreateWarmPool_InvalidExplicitID(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t, sandboxTemplate("base"))
	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "Bad ID",
	})
	if err == nil {
		t.Fatal("CreateWarmPool() = nil, want invalid-ID error")
	}
}

func TestCreateWarmPool_TemplateMapping(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t, sandboxTemplate("real-tmpl"))
	a.templateToSandboxTemplate["e2b-tmpl"] = "real-tmpl"
	wp, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "e2b-tmpl", TargetSize: 1, WarmPoolID: "wp-mapped",
	})
	if err != nil {
		t.Fatalf("CreateWarmPool() error = %v", err)
	}
	if wp.TemplateID != "e2b-tmpl" {
		t.Errorf("TemplateID = %q, want e2b-tmpl (E2B side)", wp.TemplateID)
	}
}

func TestGetWarmPool_Found(t *testing.T) {
	replicas := int32(3)
	swp := &extv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{Name: "wp-1", Namespace: "default", Generation: 1},
		Spec: extv1beta1.SandboxWarmPoolSpec{
			Replicas:    &replicas,
			TemplateRef: extv1beta1.SandboxTemplateRef{Name: "base"},
		},
		Status: extv1beta1.SandboxWarmPoolStatus{ReadyReplicas: 3, ObservedGeneration: 1},
	}
	a, _ := newWarmPoolTestAdapter(t, swp)
	wp, err := a.GetWarmPool(context.Background(), "wp-1")
	if err != nil {
		t.Fatalf("GetWarmPool() error = %v", err)
	}
	if wp.Status != "ready" || wp.CurrentSize != 3 || wp.TargetSize != 3 {
		t.Errorf("GetWarmPool() = %+v, want status=ready sizes=3/3", wp)
	}
}

func TestGetWarmPool_NotFound(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t)
	_, err := a.GetWarmPool(context.Background(), "nope")
	if !errors.Is(err, adapter.ErrWarmPoolNotFound) {
		t.Errorf("GetWarmPool() err = %v, want ErrWarmPoolNotFound", err)
	}
}

func TestWarmPoolStatus_Derivation(t *testing.T) {
	ptr := func(i int32) *int32 { return &i }
	tests := []struct {
		name   string
		spec   *int32
		gen    int64
		ready  int32
		obsGen int64
		want   string
	}{
		{"zero replicas is ready", ptr(0), 1, 0, 1, "ready"},
		{"not yet observed", ptr(3), 2, 0, 1, "pending"},
		{"all ready", ptr(3), 1, 3, 1, "ready"},
		{"partially ready", ptr(3), 1, 1, 1, "scaling"},
		{"none ready", ptr(3), 1, 0, 1, "pending"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			swp := &extv1beta1.SandboxWarmPool{
				ObjectMeta: metav1.ObjectMeta{Generation: tt.gen},
				Spec:       extv1beta1.SandboxWarmPoolSpec{Replicas: tt.spec},
				Status:     extv1beta1.SandboxWarmPoolStatus{ReadyReplicas: tt.ready, ObservedGeneration: tt.obsGen},
			}
			if got := warmPoolStatus(swp); got != tt.want {
				t.Errorf("warmPoolStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/adapter/agentsandbox/ -run 'WarmPool' -v`
Expected: 编译失败（`a.maxWarmPoolSize` undefined、`warmPoolStatus` undefined；`CreateWarmPool` 已存在但为 stub 返回 not-supported 错误 → 测试 FAIL）

- [ ] **Step 3: 实现**

3a. `adapter.go` — `AdapterConfig` 增加字段（放在 `TemplateToWarmPool` 之后）：

```go
	// WarmPoolUpdateStrategy is the SandboxWarmPool updateStrategy.type
	// ("OnReplenish" default, or "Recreate") applied to pools created via
	// CreateWarmPool.
	WarmPoolUpdateStrategy string
	// MaxWarmPoolSize caps TargetSize for CreateWarmPool/UpdateWarmPoolSize.
	// Defaults to adapter.DefaultMaxWarmPoolSize (256) when <= 0.
	MaxWarmPoolSize int
	// TemplateToSandboxTemplate maps E2B template IDs to SandboxTemplate
	// resource names. When absent, the template ID is used as the ref name.
	TemplateToSandboxTemplate map[string]string
```

`Adapter` 结构体增加（`warmPoolMap` 声明之后）：

```go
	maxWarmPoolSize           int
	warmPoolUpdateStrategy    string
	templateToSandboxTemplate map[string]string
```

`New()` 中 `maps.Copy(warmPoolMap, cfg.TemplateToWarmPool)` 之后装配：

```go
	maxSize := cfg.MaxWarmPoolSize
	if maxSize <= 0 {
		maxSize = adapter.DefaultMaxWarmPoolSize
	}
	t2st := make(map[string]string)
	maps.Copy(t2st, cfg.TemplateToSandboxTemplate)
```

并把 `maxWarmPoolSize: maxSize, warmPoolUpdateStrategy: cfg.WarmPoolUpdateStrategy, templateToSandboxTemplate: t2st` 加进 `New()` 返回的 `&Adapter{...}` 字面量。`New()` 开头校验 strategy：

```go
	switch cfg.WarmPoolUpdateStrategy {
	case "", "OnReplenish", "Recreate":
	default:
		return nil, fmt.Errorf("warmPoolUpdateStrategy must be OnReplenish or Recreate, got %q", cfg.WarmPoolUpdateStrategy)
	}
```

删除 `adapter.go` 中 `CreateWarmPool` / `GetWarmPool` 两个 stub（原 1179-1185 行；`ListWarmPools/DeleteWarmPool/UpdateWarmPoolSize` stub 留给 Task 3 删）。

3b. `factory.go` — `parseBackendConfig` 末尾追加（含本地 int 助手）：

```go
	cfg.WarmPoolUpdateStrategy = util.StringVal(raw, "warmpoolupdatestrategy", "warmPoolUpdateStrategy")
	cfg.MaxWarmPoolSize = intVal(raw, adapter.DefaultMaxWarmPoolSize, "maxwarmpoolsize", "maxWarmPoolSize")
	if t2t, ok := mapVal(raw, "templatetosandboxtemplate", "templateToSandboxTemplate"); ok {
		cfg.TemplateToSandboxTemplate = t2t
	}
```

```go
// intVal returns an int from the first matching key in m, or defaultValue.
// Handles both int (YAML) and float64 (JSON/Viper) representations.
func intVal(m map[string]any, defaultValue int, keys ...string) int {
	for _, k := range keys {
		switch v := m[k].(type) {
		case int:
			return v
		case float64:
			return int(v)
		}
	}
	return defaultValue
}
```

3c. 新建 `warmpool.go`：

```go
package agentsandbox

import (
	"context"
	"fmt"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
)

// CreateWarmPool creates a SandboxWarmPool CRD in the adapter namespace.
func (a *Adapter) CreateWarmPool(ctx context.Context, req *adapter.WarmPoolCreateRequest) (*adapter.WarmPool, error) {
	if req == nil || req.TemplateID == "" {
		return nil, fmt.Errorf("templateID is required")
	}
	size, err := adapter.ClampTargetSize(req.TargetSize, a.maxWarmPoolSize)
	if err != nil {
		return nil, err
	}
	id := req.WarmPoolID
	if id == "" {
		id = adapter.GenerateWarmPoolID(req.TemplateID)
	}
	if err := adapter.ValidateWarmPoolID(id); err != nil {
		return nil, err
	}

	// Validate the referenced SandboxTemplate exists so the controller does
	// not sit on an unresolvable ref.
	ref := a.resolveTemplateRef(req.TemplateID)
	if _, err := a.k8s.ExtensionsClient.SandboxTemplates(a.namespace).Get(ctx, ref, metav1.GetOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, fmt.Errorf("SandboxTemplate %q not found in namespace %q: create it before the warm pool", ref, a.namespace)
		}
		return nil, fmt.Errorf("checking SandboxTemplate %q: %w", ref, err)
	}

	replicas := int32(size)
	swp := &extv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: a.namespace},
		Spec: extv1beta1.SandboxWarmPoolSpec{
			Replicas:    &replicas,
			TemplateRef: extv1beta1.SandboxTemplateRef{Name: ref},
		},
	}
	if a.warmPoolUpdateStrategy != "" {
		swp.Spec.UpdateStrategy = &extv1beta1.SandboxWarmPoolUpdateStrategy{
			Type: extv1beta1.SandboxWarmPoolUpdateStrategyType(a.warmPoolUpdateStrategy),
		}
	}
	created, err := a.k8s.ExtensionsClient.SandboxWarmPools(a.namespace).Create(ctx, swp, metav1.CreateOptions{})
	if err != nil {
		if k8serrors.IsAlreadyExists(err) {
			return nil, adapter.ErrWarmPoolAlreadyExists
		}
		return nil, fmt.Errorf("creating SandboxWarmPool %q: %w", id, err)
	}

	// Make the new pool immediately consumable by CreateSandbox without a
	// gateway restart.
	a.warmPoolMapMu.Lock()
	a.warmPoolMap[req.TemplateID] = id
	a.warmPoolMapMu.Unlock()

	wp := a.toAdapterWarmPool(created)
	wp.TemplateID = req.TemplateID // E2B-side ID, not the CRD ref name
	return wp, nil
}

// GetWarmPool reads a SandboxWarmPool CRD by name.
func (a *Adapter) GetWarmPool(ctx context.Context, warmPoolID string) (*adapter.WarmPool, error) {
	swp, err := a.k8s.ExtensionsClient.SandboxWarmPools(a.namespace).Get(ctx, warmPoolID, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, adapter.ErrWarmPoolNotFound
		}
		return nil, fmt.Errorf("getting SandboxWarmPool %q: %w", warmPoolID, err)
	}
	return a.toAdapterWarmPool(swp), nil
}

// resolveTemplateRef maps an E2B template ID to a SandboxTemplate name.
func (a *Adapter) resolveTemplateRef(templateID string) string {
	if ref, ok := a.templateToSandboxTemplate[templateID]; ok {
		return ref
	}
	return templateID
}

// templateIDForRef reverses resolveTemplateRef for read paths. Falls back to
// the ref name when no mapping matches.
func (a *Adapter) templateIDForRef(ref string) string {
	for tmpl, r := range a.templateToSandboxTemplate {
		if r == ref {
			return tmpl
		}
	}
	return ref
}

// toAdapterWarmPool converts the CRD to the gateway DTO.
func (a *Adapter) toAdapterWarmPool(swp *extv1beta1.SandboxWarmPool) *adapter.WarmPool {
	target := 0
	if swp.Spec.Replicas != nil {
		target = int(*swp.Spec.Replicas)
	}
	return &adapter.WarmPool{
		WarmPoolID:  swp.Name,
		TemplateID:  a.templateIDForRef(swp.Spec.TemplateRef.Name),
		TargetSize:  target,
		CurrentSize: int(swp.Status.ReadyReplicas),
		Status:      warmPoolStatus(swp),
		CreatedAt:   swp.CreationTimestamp.Time,
	}
}

// warmPoolStatus derives the unified status vocabulary from CRD fields.
func warmPoolStatus(swp *extv1beta1.SandboxWarmPool) string {
	target := int32(0)
	if swp.Spec.Replicas != nil {
		target = *swp.Spec.Replicas
	}
	switch {
	case target == 0:
		return "ready"
	case swp.Status.ObservedGeneration < swp.Generation:
		return "pending"
	case swp.Status.ReadyReplicas >= target:
		return "ready"
	case swp.Status.ReadyReplicas > 0:
		return "scaling"
	default:
		return "pending"
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test -race ./internal/adapter/agentsandbox/ -run 'WarmPool' -v`
Expected: 全部 PASS（List/Delete/UpdateSize stub 仍编译存在，Task 3 处理）

- [ ] **Step 5: 提交**

```bash
git add internal/adapter/agentsandbox/
git commit -m "feat(agentsandbox): implement warm pool Create/Get via SandboxWarmPool CRD"
```

---

### Task 3: agentsandbox — List / Delete / UpdateWarmPoolSize

**Files:**
- Modify: `internal/adapter/agentsandbox/warmpool.go`（Task 2 创建）
- Modify: `internal/adapter/agentsandbox/adapter.go`（删除剩余 3 个 stub，原 1173-1177、1187-1194 行）
- Test: `internal/adapter/agentsandbox/warmpool_test.go`（追加）

**Interfaces:**
- Consumes: Task 2 的 `newWarmPoolTestAdapter`、`toAdapterWarmPool`；Task 1 哨兵错误
- Produces: 完整的 `SandboxAdapter` warm pool 5 方法（agentsandbox 侧）

- [ ] **Step 1: 追加失败测试**

```go
func TestListWarmPools(t *testing.T) {
	r2 := int32(2)
	objs := []runtime.Object{
		&extv1beta1.SandboxWarmPool{
			ObjectMeta: metav1.ObjectMeta{Name: "wp-a", Namespace: "default", Generation: 1},
			Spec: extv1beta1.SandboxWarmPoolSpec{
				Replicas: &r2, TemplateRef: extv1beta1.SandboxTemplateRef{Name: "base"},
			},
			Status: extv1beta1.SandboxWarmPoolStatus{ReadyReplicas: 2, ObservedGeneration: 1},
		},
		&extv1beta1.SandboxWarmPool{
			ObjectMeta: metav1.ObjectMeta{Name: "wp-b", Namespace: "default", Generation: 1},
			Spec: extv1beta1.SandboxWarmPoolSpec{
				Replicas: &r2, TemplateRef: extv1beta1.SandboxTemplateRef{Name: "code"},
			},
			Status: extv1beta1.SandboxWarmPoolStatus{ReadyReplicas: 0, ObservedGeneration: 1},
		},
	}
	a, _ := newWarmPoolTestAdapter(t, objs...)
	pools, err := a.ListWarmPools(context.Background())
	if err != nil {
		t.Fatalf("ListWarmPools() error = %v", err)
	}
	if len(pools) != 2 {
		t.Fatalf("ListWarmPools() returned %d pools, want 2", len(pools))
	}
	byID := map[string]*adapter.WarmPool{pools[0].WarmPoolID: pools[0], pools[1].WarmPoolID: pools[1]}
	if byID["wp-a"].Status != "ready" || byID["wp-b"].Status != "pending" {
		t.Errorf("statuses = %q/%q, want ready/pending", byID["wp-a"].Status, byID["wp-b"].Status)
	}
}

func TestListWarmPools_Empty(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t)
	pools, err := a.ListWarmPools(context.Background())
	if err != nil {
		t.Fatalf("ListWarmPools() error = %v", err)
	}
	if pools == nil || len(pools) != 0 {
		t.Errorf("ListWarmPools() = %v, want empty non-nil slice", pools)
	}
}

func TestDeleteWarmPool(t *testing.T) {
	r1 := int32(1)
	swp := &extv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{Name: "wp-del", Namespace: "default"},
		Spec: extv1beta1.SandboxWarmPoolSpec{
			Replicas: &r1, TemplateRef: extv1beta1.SandboxTemplateRef{Name: "base"},
		},
	}
	a, cs := newWarmPoolTestAdapter(t, swp)
	a.warmPoolMap["base"] = "wp-del"

	if err := a.DeleteWarmPool(context.Background(), "wp-del"); err != nil {
		t.Fatalf("DeleteWarmPool() error = %v", err)
	}
	if _, err := cs.ExtensionsV1beta1().SandboxWarmPools("default").
		Get(context.Background(), "wp-del", metav1.GetOptions{}); !k8serrors.IsNotFound(err) {
		t.Errorf("CRD still exists after delete (err = %v)", err)
	}
	// 消费映射同步清理，CreateSandbox 不再路由到已删池
	if _, ok := a.warmPoolMap["base"]; ok {
		t.Error("warmPoolMap still references deleted pool")
	}
}

func TestDeleteWarmPool_NotFound(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t)
	if err := a.DeleteWarmPool(context.Background(), "nope"); !errors.Is(err, adapter.ErrWarmPoolNotFound) {
		t.Errorf("DeleteWarmPool() err = %v, want ErrWarmPoolNotFound", err)
	}
}

func TestUpdateWarmPoolSize(t *testing.T) {
	r1 := int32(1)
	swp := &extv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{Name: "wp-up", Namespace: "default"},
		Spec: extv1beta1.SandboxWarmPoolSpec{
			Replicas: &r1, TemplateRef: extv1beta1.SandboxTemplateRef{Name: "base"},
		},
	}
	a, cs := newWarmPoolTestAdapter(t, swp)
	if err := a.UpdateWarmPoolSize(context.Background(), "wp-up", 5); err != nil {
		t.Fatalf("UpdateWarmPoolSize() error = %v", err)
	}
	got, err := cs.ExtensionsV1beta1().SandboxWarmPools("default").
		Get(context.Background(), "wp-up", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after patch: %v", err)
	}
	if *got.Spec.Replicas != 5 {
		t.Errorf("spec.replicas = %d, want 5", *got.Spec.Replicas)
	}
}

func TestUpdateWarmPoolSize_NegativeRejected(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t)
	if err := a.UpdateWarmPoolSize(context.Background(), "wp-up", -1); err == nil {
		t.Error("UpdateWarmPoolSize(-1) = nil, want error")
	}
}

func TestUpdateWarmPoolSize_NotFound(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t)
	if err := a.UpdateWarmPoolSize(context.Background(), "nope", 3); !errors.Is(err, adapter.ErrWarmPoolNotFound) {
		t.Errorf("UpdateWarmPoolSize() err = %v, want ErrWarmPoolNotFound", err)
	}
}
```

测试文件 import 增加 `k8serrors "k8s.io/apimachinery/pkg/api/errors"`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/adapter/agentsandbox/ -run 'TestListWarmPools|TestDeleteWarmPool|TestUpdateWarmPoolSize' -v`
Expected: FAIL（stub 返回 not-supported / 空列表）

- [ ] **Step 3: 实现** — 删除 `adapter.go` 剩余 3 个 stub，`warmpool.go` 追加：

```go
// ListWarmPools lists all SandboxWarmPool CRDs in the adapter namespace.
func (a *Adapter) ListWarmPools(ctx context.Context) ([]*adapter.WarmPool, error) {
	list, err := a.k8s.ExtensionsClient.SandboxWarmPools(a.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing SandboxWarmPools: %w", err)
	}
	pools := make([]*adapter.WarmPool, 0, len(list.Items))
	for i := range list.Items {
		pools = append(pools, a.toAdapterWarmPool(&list.Items[i]))
	}
	return pools, nil
}

// DeleteWarmPool deletes the SandboxWarmPool CRD. The controller reclaims
// unclaimed sandboxes per its updateStrategy; claimed sandboxes are untouched.
func (a *Adapter) DeleteWarmPool(ctx context.Context, warmPoolID string) error {
	err := a.k8s.ExtensionsClient.SandboxWarmPools(a.namespace).Delete(ctx, warmPoolID, metav1.DeleteOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return adapter.ErrWarmPoolNotFound
		}
		return fmt.Errorf("deleting SandboxWarmPool %q: %w", warmPoolID, err)
	}
	a.warmPoolMapMu.Lock()
	for tmpl, pool := range a.warmPoolMap {
		if pool == warmPoolID {
			delete(a.warmPoolMap, tmpl)
		}
	}
	a.warmPoolMapMu.Unlock()
	return nil
}

// UpdateWarmPoolSize patches spec.replicas. The controller reconciles.
func (a *Adapter) UpdateWarmPoolSize(ctx context.Context, warmPoolID string, targetSize int) error {
	size, err := adapter.ClampTargetSize(targetSize, a.maxWarmPoolSize)
	if err != nil {
		return err
	}
	patch := []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, size))
	_, err = a.k8s.ExtensionsClient.SandboxWarmPools(a.namespace).Patch(
		ctx, warmPoolID, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return adapter.ErrWarmPoolNotFound
		}
		return fmt.Errorf("patching SandboxWarmPool %q: %w", warmPoolID, err)
	}
	return nil
}
```

import 增加 `"k8s.io/apimachinery/pkg/types"`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test -race ./internal/adapter/agentsandbox/ -v`
Expected: 全部 PASS（含既有测试无回归）

- [ ] **Step 5: 提交**

```bash
git add internal/adapter/agentsandbox/
git commit -m "feat(agentsandbox): implement warm pool List/Delete/UpdateSize"
```

---

### Task 4: opensandbox — 池基础设施（warmPool 接口 + 配置 + resolveImageURI 提取）

**Files:**
- Create: `internal/adapter/opensandbox/warmpool.go`
- Modify: `internal/adapter/opensandbox/adapter.go`（`AdapterConfig`/`Adapter` 增字段、`New()` 装配、`CreateSandbox` 中镜像解析链提取为 `resolveImageURI`）
- Modify: `internal/adapter/opensandbox/factory.go:28-72`（`pool` 配置段解析）
- Test: `internal/adapter/opensandbox/warmpool_test.go`

**Interfaces:**
- Consumes: Task 1 全部产物
- Produces（Task 5/6 依赖，签名必须一致）:
  - `type warmPool interface { Start(ctx context.Context) error; AcquireID(ctx context.Context) (string, error); Resize(ctx context.Context, newMaxIdle int) error; Snapshot(ctx context.Context) (*opensandbox.PoolSnapshot, error); Shutdown(ctx context.Context, graceful bool) error }`
  - `type poolEntry struct { pool warmPool; templateID string; targetSize int; createdAt time.Time }`
  - `Adapter` 新字段：`pools map[string]*poolEntry`、`poolsMu sync.RWMutex`、`maxWarmPoolSize int`、`poolStoreCfg *PoolStateStoreConfig`、`newPoolFn func(ctx context.Context, poolID, imageURI string, size int) (warmPool, error)`
  - `type PoolStateStoreConfig struct { Type, Addr, Password string; DB int; KeyPrefix string }`
  - `func (a *Adapter) resolveImageURI(ctx context.Context, templateID string) (string, error)`
  - `func (a *Adapter) buildPoolStateStore() (opensandbox.PoolStateStore, error)`
  - `func poolStatus(snap *opensandbox.PoolSnapshot) string`

设计要点：SDK 的 `Acquire` 返回 `*opensandbox.Sandbox`（私有字段，测试无法构造），所以 adapter 侧接口收窄为 `AcquireID(ctx) (string, error)`；`sdkWarmPool` 包装器把 `opensandbox.SandboxPool` 适配到该接口，`CreateSandbox` 拿到 ID 后走现有 `a.lifecycle.GetSandbox`（已有 mock）补齐元数据。

- [ ] **Step 1: 写失败测试**（本 Task 只测基础设施）

```go
package opensandbox

import (
	"context"
	"testing"

	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
)

func TestResolveImageURI_StaticMap(t *testing.T) {
	a := &Adapter{templateToImage: map[string]string{"base": "registry.example.com/base:v1"}}
	got, err := a.resolveImageURI(context.Background(), "base")
	if err != nil {
		t.Fatalf("resolveImageURI() error = %v", err)
	}
	if got != "registry.example.com/base:v1" {
		t.Errorf("resolveImageURI() = %q, want registry.example.com/base:v1", got)
	}
}

func TestResolveImageURI_FallbackToTemplateID(t *testing.T) {
	a := &Adapter{templateToImage: map[string]string{}}
	got, err := a.resolveImageURI(context.Background(), "some/image:latest")
	if err != nil {
		t.Fatalf("resolveImageURI() error = %v", err)
	}
	if got != "some/image:latest" {
		t.Errorf("resolveImageURI() = %q, want some/image:latest", got)
	}
}

func TestPoolConnectionConfig(t *testing.T) {
	a := &Adapter{baseURL: "https://sandbox.example.com:8443", apiKey: "k"}
	cc, err := a.poolConnectionConfig()
	if err != nil {
		t.Fatalf("poolConnectionConfig() error = %v", err)
	}
	if cc.Protocol != "https" || cc.Domain != "sandbox.example.com:8443" || cc.APIKey != "k" {
		t.Errorf("poolConnectionConfig() = %+v", cc)
	}
}

func TestPoolConnectionConfig_Invalid(t *testing.T) {
	a := &Adapter{baseURL: "not a url"}
	if _, err := a.poolConnectionConfig(); err == nil {
		t.Error("poolConnectionConfig() = nil error, want parse error")
	}
}

func TestPoolStatus_Mapping(t *testing.T) {
	tests := []struct {
		name string
		snap *opensandbox.PoolSnapshot
		want string
	}{
		{"running full", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleRunning, IdleCount: 3, MaxIdle: 3}, "ready"},
		{"running filling", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleRunning, IdleCount: 1, MaxIdle: 3}, "scaling"},
		{"running degraded", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleRunning, HealthState: opensandbox.PoolDegraded,
			IdleCount: 3, MaxIdle: 3}, "degraded"},
		{"starting", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleStarting}, "pending"},
		{"stopped", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleStopped}, "destroyed"},
		{"draining", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleDraining}, "destroyed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := poolStatus(tt.snap); got != tt.want {
				t.Errorf("poolStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildPoolStateStore_DefaultMemory(t *testing.T) {
	a := &Adapter{}
	store, err := a.buildPoolStateStore()
	if err != nil {
		t.Fatalf("buildPoolStateStore() error = %v", err)
	}
	if store != nil {
		t.Error("buildPoolStateStore() with no config = non-nil, want nil (SDK memory default)")
	}
}

func TestBuildPoolStateStore_RedisRequiresAddr(t *testing.T) {
	a := &Adapter{poolStoreCfg: &PoolStateStoreConfig{Type: "redis"}}
	if _, err := a.buildPoolStateStore(); err == nil {
		t.Error("buildPoolStateStore() redis without addr = nil error")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/adapter/opensandbox/ -run 'TestResolveImageURI|TestPoolConnectionConfig|TestPoolStatus|TestBuildPoolStateStore' -v`
Expected: 编译失败（`resolveImageURI`、`poolConnectionConfig`、`poolStatus`、`PoolStateStoreConfig` undefined）

- [ ] **Step 3: 实现**

3a. `warmpool.go` 新建（本 Task 只含基础设施，CRUD 在 Task 5 追加到同文件）：

```go
package opensandbox

import (
	"context"
	"fmt"
	"net/url"

	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
	"github.com/alibaba/OpenSandbox/sdks/sandbox/go/poolredis"
	"github.com/redis/go-redis/v9"
)

// warmPool is the adapter-facing subset of opensandbox.SandboxPool.
// AcquireID narrows Acquire's *Sandbox return (unexported fields, not
// constructible in tests) to the sandbox ID the adapter needs.
type warmPool interface {
	Start(ctx context.Context) error
	AcquireID(ctx context.Context) (string, error)
	Resize(ctx context.Context, newMaxIdle int) error
	Snapshot(ctx context.Context) (*opensandbox.PoolSnapshot, error)
	Shutdown(ctx context.Context, graceful bool) error
}

// sdkWarmPool adapts an opensandbox.SandboxPool to warmPool.
type sdkWarmPool struct{ inner opensandbox.SandboxPool }

var _ warmPool = (*sdkWarmPool)(nil)

func (p *sdkWarmPool) Start(ctx context.Context) error { return p.inner.Start(ctx) }

func (p *sdkWarmPool) AcquireID(ctx context.Context) (string, error) {
	sb, err := p.inner.Acquire(ctx, opensandbox.AcquireOptions{})
	if err != nil {
		return "", err
	}
	return sb.ID(), nil
}

func (p *sdkWarmPool) Resize(ctx context.Context, n int) error { return p.inner.Resize(ctx, n) }

func (p *sdkWarmPool) Snapshot(ctx context.Context) (*opensandbox.PoolSnapshot, error) {
	return p.inner.Snapshot(ctx)
}

func (p *sdkWarmPool) Shutdown(ctx context.Context, graceful bool) error {
	return p.inner.Shutdown(ctx, graceful)
}

// poolEntry is a registered warm pool.
type poolEntry struct {
	pool       warmPool
	templateID string
	targetSize int
	createdAt  time.Time
}

// PoolStateStoreConfig configures the opensandbox pool state store.
type PoolStateStoreConfig struct {
	Type      string // "memory" (default) | "redis"
	Addr      string
	Password  string
	DB        int
	KeyPrefix string
}

// poolConnectionConfig converts the adapter baseURL/apiKey into the SDK's
// ConnectionConfig.
func (a *Adapter) poolConnectionConfig() (opensandbox.ConnectionConfig, error) {
	u, err := url.Parse(a.baseURL)
	if err != nil || u.Host == "" {
		return opensandbox.ConnectionConfig{}, fmt.Errorf("baseURL %q is not a valid http(s) URL", a.baseURL)
	}
	return opensandbox.ConnectionConfig{
		Domain:   u.Host,
		Protocol: u.Scheme,
		APIKey:   a.apiKey,
	}, nil
}

// buildPoolStateStore returns nil for the SDK memory default, or a Redis
// store when configured.
func (a *Adapter) buildPoolStateStore() (opensandbox.PoolStateStore, error) {
	if a.poolStoreCfg == nil || a.poolStoreCfg.Type == "" || a.poolStoreCfg.Type == "memory" {
		return nil, nil
	}
	if a.poolStoreCfg.Type != "redis" {
		return nil, fmt.Errorf("unknown pool stateStore type %q (supported: memory, redis)", a.poolStoreCfg.Type)
	}
	if a.poolStoreCfg.Addr == "" {
		return nil, fmt.Errorf("pool stateStore redis requires addr")
	}
	client := redis.NewClient(&redis.Options{
		Addr:     a.poolStoreCfg.Addr,
		Password: a.poolStoreCfg.Password,
		DB:       a.poolStoreCfg.DB,
	})
	keyPrefix := a.poolStoreCfg.KeyPrefix
	if keyPrefix == "" {
		keyPrefix = "opensandbox:pool"
	}
	return poolredis.NewRedisPoolStateStore(poolredis.RedisPoolStateStoreConfig{
		Client:    client,
		KeyPrefix: keyPrefix,
	})
}

// poolStatus maps SDK pool state to the unified status vocabulary.
func poolStatus(snap *opensandbox.PoolSnapshot) string {
	switch snap.LifecycleState {
	case opensandbox.PoolLifecycleRunning:
		if snap.HealthState == opensandbox.PoolDegraded {
			return "degraded"
		}
		if snap.IdleCount < snap.MaxIdle {
			return "scaling"
		}
		return "ready"
	case opensandbox.PoolLifecycleStarting, opensandbox.PoolLifecycleNotStarted:
		return "pending"
	case opensandbox.PoolLifecycleDraining, opensandbox.PoolLifecycleStopped:
		return "destroyed"
	default:
		return "pending"
	}
}

// defaultNewPool builds a real SDK pool. It is the production value of
// Adapter.newPoolFn; tests substitute fakes.
func (a *Adapter) defaultNewPool(ctx context.Context, poolID, imageURI string, size int) (warmPool, error) {
	cc, err := a.poolConnectionConfig()
	if err != nil {
		return nil, err
	}
	timeout := opensandbox.DefaultTimeoutSeconds
	b := opensandbox.NewSandboxPoolBuilder().
		PoolName(poolID).
		MaxIdle(size).
		ConnectionConfig(cc).
		CreationSpec(opensandbox.PoolCreationSpec{
			Image:          imageURI,
			Entrypoint:     opensandbox.DefaultEntrypoint,
			ResourceLimits: opensandbox.DefaultResourceLimits,
			TimeoutSeconds: &timeout,
		})
	if store, err := a.buildPoolStateStore(); err != nil {
		return nil, err
	} else if store != nil {
		b = b.StateStore(store)
	}
	p, err := b.Build()
	if err != nil {
		return nil, fmt.Errorf("building pool %q: %w", poolID, err)
	}
	return &sdkWarmPool{inner: p}, nil
}
```

（import 需含 `"time"`，`redis` 模块路径以仓库 `go.mod` 现有 go-redis 依赖为准——`redis_store.go` 已导入同款，直接对齐它的 import 路径。）

3b. `adapter.go`：

- `Adapter` 结构体追加字段（`templates` 之后）：

```go
	// pools is the in-process warm pool registry keyed by warmPoolID.
	pools   map[string]*poolEntry
	poolsMu sync.RWMutex

	// maxWarmPoolSize caps TargetSize (DoS protection).
	maxWarmPoolSize int

	// poolStoreCfg configures the pool state store (nil = SDK memory default).
	poolStoreCfg *PoolStateStoreConfig

	// newPoolFn builds pools; tests substitute fakes.
	newPoolFn func(ctx context.Context, poolID, imageURI string, size int) (warmPool, error)
```

- `AdapterConfig` 追加：`MaxWarmPoolSize int`、`PoolStateStore *PoolStateStoreConfig`
- `New()`：字面量中追加 `pools: make(map[string]*poolEntry)`、`maxWarmPoolSize`（<=0 时取 `adapter.DefaultMaxWarmPoolSize`）、`poolStoreCfg: cfg.PoolStateStore`；构造后设置 `a.newPoolFn = a.defaultNewPool`（需要把 `return &Adapter{...}` 改为先赋值变量 `a := &Adapter{...}`，设 `a.newPoolFn = a.defaultNewPool`，再 `return a, nil`）
- **提取 `resolveImageURI`**：把 `CreateSandbox` 中从 `imageURI := req.TemplateID` 到 alias 解析结束的整段（现有注释保留）原样搬进新方法，签名 `func (a *Adapter) resolveImageURI(ctx context.Context, templateID string) (string, error)`；`CreateSandbox` 改为：

```go
	imageURI, err := a.resolveImageURI(ctx, req.TemplateID)
	if err != nil {
		return nil, err
	}
```

3c. `factory.go`：`NewAdapterFromConfig` 中 `templateStore` 解析之后追加：

```go
	// Optional pool configuration (warm pool CRUD + Acquire).
	if poolCfg, ok := lookupMap(bcfg.Config, "pool"); ok {
		if v, ok := lookupInt(poolCfg, "maxwarmpoolsize", "maxWarmPoolSize"); ok {
			cfg.MaxWarmPoolSize = v
		}
		if storeCfg, ok := lookupMap(poolCfg, "statestore", "stateStore"); ok {
			sc := &PoolStateStoreConfig{}
			sc.Type, _ = lookupString(storeCfg, "type")
			if sc.Type == "" {
				sc.Type = "memory"
			}
			sc.Addr, _ = lookupString(storeCfg, "addr", "address")
			sc.Password, _ = lookupString(storeCfg, "password")
			if v, ok := lookupInt(storeCfg, "db"); ok {
				sc.DB = v
			}
			sc.KeyPrefix, _ = lookupString(storeCfg, "keyprefix", "keyPrefix")
			cfg.PoolStateStore = sc
		}
	}
```

（`lookupInt` 已存在于本包 factory.go，供 `buildRedisStore` 使用。）

- [ ] **Step 4: 跑测试确认通过**

Run: `go test -race ./internal/adapter/opensandbox/ -v`
Expected: 新测试 PASS，且既有 `CreateSandbox` 相关测试无回归（resolveImageURI 是纯提取）

- [ ] **Step 5: 提交**

```bash
git add internal/adapter/opensandbox/
git commit -m "refactor(opensandbox): extract resolveImageURI and add warm pool infrastructure"
```

---

### Task 5: opensandbox — CRUD 5 方法

**Files:**
- Modify: `internal/adapter/opensandbox/warmpool.go`（追加 5 个方法）
- Modify: `internal/adapter/opensandbox/adapter.go:803-822`（删除 5 个 stub）
- Test: `internal/adapter/opensandbox/warmpool_test.go`（追加）

**Interfaces:**
- Consumes: Task 4 的 `warmPool`/`poolEntry`/`newPoolFn`/`resolveImageURI`/`poolStatus`；Task 1 哨兵错误与校验
- Produces: 完整 `SandboxAdapter` warm pool 5 方法（opensandbox 侧）；`func (a *Adapter) lookupPoolByTemplate(templateID string) *poolEntry`（Task 6 用）

- [ ] **Step 1: 追加失败测试**

```go
import (
	"errors"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
)

// fakeWarmPool implements warmPool for tests.
type fakeWarmPool struct {
	startErr   error
	acquireID  string
	acquireErr error
	snap       *opensandbox.PoolSnapshot
	resizeErr  error
	resizedTo  int
	shutdowns  int
}

func (f *fakeWarmPool) Start(context.Context) error { return f.startErr }
func (f *fakeWarmPool) AcquireID(context.Context) (string, error) {
	return f.acquireID, f.acquireErr
}
func (f *fakeWarmPool) Resize(_ context.Context, n int) error {
	f.resizedTo = n
	return f.resizeErr
}
func (f *fakeWarmPool) Snapshot(context.Context) (*opensandbox.PoolSnapshot, error) {
	if f.snap == nil {
		return &opensandbox.PoolSnapshot{LifecycleState: opensandbox.PoolLifecycleRunning}, nil
	}
	return f.snap, nil
}
func (f *fakeWarmPool) Shutdown(context.Context, bool) error {
	f.shutdowns++
	return nil
}

func newPoolTestAdapter(fake *fakeWarmPool) *Adapter {
	a := &Adapter{
		name:            "opensandbox",
		baseURL:         "http://localhost:8080",
		templateToImage: map[string]string{"base": "registry/base:v1"},
		pools:           make(map[string]*poolEntry),
		maxWarmPoolSize: 256,
	}
	a.newPoolFn = func(context.Context, string, string, int) (warmPool, error) {
		return fake, nil
	}
	return a
}

func TestCreateWarmPool_Opensandbox(t *testing.T) {
	fake := &fakeWarmPool{}
	a := newPoolTestAdapter(fake)
	wp, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 3, WarmPoolID: "wp-base-1",
	})
	if err != nil {
		t.Fatalf("CreateWarmPool() error = %v", err)
	}
	if wp.WarmPoolID != "wp-base-1" || wp.TemplateID != "base" || wp.TargetSize != 3 {
		t.Errorf("CreateWarmPool() = %+v", wp)
	}
	if _, ok := a.pools["wp-base-1"]; !ok {
		t.Error("pool not registered")
	}
}

func TestCreateWarmPool_Duplicate(t *testing.T) {
	a := newPoolTestAdapter(&fakeWarmPool{})
	req := &adapter.WarmPoolCreateRequest{TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-dup"}
	if _, err := a.CreateWarmPool(context.Background(), req); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := a.CreateWarmPool(context.Background(), req)
	if !errors.Is(err, adapter.ErrWarmPoolAlreadyExists) {
		t.Errorf("second create err = %v, want ErrWarmPoolAlreadyExists", err)
	}
}

func TestCreateWarmPool_StartError(t *testing.T) {
	a := newPoolTestAdapter(&fakeWarmPool{startErr: errors.New("boom")})
	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-x",
	})
	if err == nil {
		t.Fatal("CreateWarmPool() = nil, want start error")
	}
	if _, ok := a.pools["wp-x"]; ok {
		t.Error("failed pool must not be registered")
	}
}

func TestGetWarmPool_Opensandbox(t *testing.T) {
	fake := &fakeWarmPool{snap: &opensandbox.PoolSnapshot{
		LifecycleState: opensandbox.PoolLifecycleRunning, IdleCount: 2, MaxIdle: 3,
	}}
	a := newPoolTestAdapter(fake)
	if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 3, WarmPoolID: "wp-g",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	wp, err := a.GetWarmPool(context.Background(), "wp-g")
	if err != nil {
		t.Fatalf("GetWarmPool() error = %v", err)
	}
	if wp.Status != "scaling" || wp.CurrentSize != 2 || wp.TargetSize != 3 {
		t.Errorf("GetWarmPool() = %+v, want scaling 2/3", wp)
	}
}

func TestGetWarmPool_NotFound(t *testing.T) {
	a := newPoolTestAdapter(&fakeWarmPool{})
	_, err := a.GetWarmPool(context.Background(), "nope")
	if !errors.Is(err, adapter.ErrWarmPoolNotFound) {
		t.Errorf("GetWarmPool() err = %v, want ErrWarmPoolNotFound", err)
	}
}

func TestListWarmPools_Opensandbox(t *testing.T) {
	a := newPoolTestAdapter(&fakeWarmPool{})
	for _, id := range []string{"wp-1", "wp-2"} {
		if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
			TemplateID: "base", TargetSize: 1, WarmPoolID: id,
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	pools, err := a.ListWarmPools(context.Background())
	if err != nil {
		t.Fatalf("ListWarmPools() error = %v", err)
	}
	if len(pools) != 2 {
		t.Errorf("ListWarmPools() returned %d, want 2", len(pools))
	}
}

func TestDeleteWarmPool_Opensandbox(t *testing.T) {
	fake := &fakeWarmPool{}
	a := newPoolTestAdapter(fake)
	if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-d",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := a.DeleteWarmPool(context.Background(), "wp-d"); err != nil {
		t.Fatalf("DeleteWarmPool() error = %v", err)
	}
	if fake.shutdowns != 1 {
		t.Errorf("Shutdown called %d times, want 1", fake.shutdowns)
	}
	if _, ok := a.pools["wp-d"]; ok {
		t.Error("pool still registered after delete")
	}
	if err := a.DeleteWarmPool(context.Background(), "wp-d"); !errors.Is(err, adapter.ErrWarmPoolNotFound) {
		t.Errorf("second delete err = %v, want ErrWarmPoolNotFound", err)
	}
}

func TestUpdateWarmPoolSize_Opensandbox(t *testing.T) {
	fake := &fakeWarmPool{}
	a := newPoolTestAdapter(fake)
	if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-u",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := a.UpdateWarmPoolSize(context.Background(), "wp-u", 7); err != nil {
		t.Fatalf("UpdateWarmPoolSize() error = %v", err)
	}
	if fake.resizedTo != 7 || a.pools["wp-u"].targetSize != 7 {
		t.Errorf("resize = %d, entry.targetSize = %d, want 7/7", fake.resizedTo, a.pools["wp-u"].targetSize)
	}
	if err := a.UpdateWarmPoolSize(context.Background(), "wp-u", -1); err == nil {
		t.Error("UpdateWarmPoolSize(-1) = nil, want error")
	}
	if err := a.UpdateWarmPoolSize(context.Background(), "nope", 1); !errors.Is(err, adapter.ErrWarmPoolNotFound) {
		t.Errorf("UpdateWarmPoolSize(nope) err = %v, want ErrWarmPoolNotFound", err)
	}
}

func TestLookupPoolByTemplate(t *testing.T) {
	a := newPoolTestAdapter(&fakeWarmPool{})
	if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-t",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if e := a.lookupPoolByTemplate("base"); e == nil {
		t.Error("lookupPoolByTemplate(base) = nil, want entry")
	}
	if e := a.lookupPoolByTemplate("other"); e != nil {
		t.Error("lookupPoolByTemplate(other) = non-nil, want nil")
	}
}

func TestWarmPoolRegistry_ConcurrentAccess(t *testing.T) {
	a := newPoolTestAdapter(&fakeWarmPool{})
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			id := fmt.Sprintf("wp-c%d", i)
			_, _ = a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
				TemplateID: "base", TargetSize: 1, WarmPoolID: id,
			})
			_, _ = a.ListWarmPools(context.Background())
			_ = a.DeleteWarmPool(context.Background(), id)
		}(i)
	}
	for i := 0; i < 8; i++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent access timed out")
		}
	}
}
```

（测试文件 import 补 `"fmt"`。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/adapter/opensandbox/ -run 'WarmPool|Pool' -v`
Expected: FAIL（stub 返回 not-supported / 空）

- [ ] **Step 3: 实现** — 删除 `adapter.go:803-822` 的 5 个 stub；`warmpool.go` 追加：

```go
// CreateWarmPool builds and starts an in-process SDK pool.
func (a *Adapter) CreateWarmPool(ctx context.Context, req *adapter.WarmPoolCreateRequest) (*adapter.WarmPool, error) {
	if req == nil || req.TemplateID == "" {
		return nil, fmt.Errorf("templateID is required")
	}
	size, err := adapter.ClampTargetSize(req.TargetSize, a.maxWarmPoolSize)
	if err != nil {
		return nil, err
	}
	id := req.WarmPoolID
	if id == "" {
		id = adapter.GenerateWarmPoolID(req.TemplateID)
	}
	if err := adapter.ValidateWarmPoolID(id); err != nil {
		return nil, err
	}

	a.poolsMu.Lock()
	if _, exists := a.pools[id]; exists {
		a.poolsMu.Unlock()
		return nil, adapter.ErrWarmPoolAlreadyExists
	}
	a.poolsMu.Unlock()

	imageURI, err := a.resolveImageURI(ctx, req.TemplateID)
	if err != nil {
		return nil, err
	}
	pool, err := a.newPoolFn(ctx, id, imageURI, size)
	if err != nil {
		return nil, fmt.Errorf("building warm pool %q: %w", id, err)
	}
	if err := pool.Start(ctx); err != nil {
		return nil, fmt.Errorf("starting warm pool %q: %w", id, err)
	}

	a.poolsMu.Lock()
	a.pools[id] = &poolEntry{
		pool:       pool,
		templateID: req.TemplateID,
		targetSize: size,
		createdAt:  time.Now(),
	}
	a.poolsMu.Unlock()

	return a.GetWarmPool(ctx, id)
}

// GetWarmPool snapshots a registered pool.
func (a *Adapter) GetWarmPool(ctx context.Context, warmPoolID string) (*adapter.WarmPool, error) {
	a.poolsMu.RLock()
	entry, ok := a.pools[warmPoolID]
	a.poolsMu.RUnlock()
	if !ok {
		return nil, adapter.ErrWarmPoolNotFound
	}
	snap, err := entry.pool.Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshotting warm pool %q: %w", warmPoolID, err)
	}
	return &adapter.WarmPool{
		WarmPoolID:  warmPoolID,
		TemplateID:  entry.templateID,
		TargetSize:  entry.targetSize,
		CurrentSize: snap.IdleCount,
		Status:      poolStatus(snap),
		CreatedAt:   entry.createdAt,
	}, nil
}

// ListWarmPools lists all registered pools.
func (a *Adapter) ListWarmPools(ctx context.Context) ([]*adapter.WarmPool, error) {
	a.poolsMu.RLock()
	ids := make([]string, 0, len(a.pools))
	for id := range a.pools {
		ids = append(ids, id)
	}
	a.poolsMu.RUnlock()

	pools := make([]*adapter.WarmPool, 0, len(ids))
	for _, id := range ids {
		wp, err := a.GetWarmPool(ctx, id)
		if err != nil {
			return nil, err
		}
		pools = append(pools, wp)
	}
	return pools, nil
}

// DeleteWarmPool gracefully shuts down a pool (releasing idle sandboxes) and
// unregisters it.
func (a *Adapter) DeleteWarmPool(ctx context.Context, warmPoolID string) error {
	a.poolsMu.Lock()
	entry, ok := a.pools[warmPoolID]
	if ok {
		delete(a.pools, warmPoolID)
	}
	a.poolsMu.Unlock()
	if !ok {
		return adapter.ErrWarmPoolNotFound
	}
	if err := entry.pool.Shutdown(ctx, true); err != nil {
		return fmt.Errorf("shutting down warm pool %q: %w", warmPoolID, err)
	}
	return nil
}

// UpdateWarmPoolSize resizes the pool's MaxIdle.
func (a *Adapter) UpdateWarmPoolSize(ctx context.Context, warmPoolID string, targetSize int) error {
	size, err := adapter.ClampTargetSize(targetSize, a.maxWarmPoolSize)
	if err != nil {
		return err
	}
	a.poolsMu.RLock()
	entry, ok := a.pools[warmPoolID]
	a.poolsMu.RUnlock()
	if !ok {
		return adapter.ErrWarmPoolNotFound
	}
	if err := entry.pool.Resize(ctx, size); err != nil {
		return fmt.Errorf("resizing warm pool %q: %w", warmPoolID, err)
	}
	a.poolsMu.Lock()
	entry.targetSize = size
	a.poolsMu.Unlock()
	return nil
}

// lookupPoolByTemplate returns the pool serving templateID, or nil.
func (a *Adapter) lookupPoolByTemplate(templateID string) *poolEntry {
	a.poolsMu.RLock()
	defer a.poolsMu.RUnlock()
	for _, entry := range a.pools {
		if entry.templateID == templateID {
			return entry
		}
	}
	return nil
}
```

`warmpool.go` import 增加 `"time"` 与 `"github.com/e2bgateway/e2bgateway/internal/adapter"`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test -race ./internal/adapter/opensandbox/ -v`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add internal/adapter/opensandbox/
git commit -m "feat(opensandbox): implement warm pool CRUD with in-process SDK pools"
```

---

### Task 6: opensandbox — CreateSandbox 自动接入 pool.Acquire

**Files:**
- Modify: `internal/adapter/opensandbox/adapter.go`（`CreateSandbox` 在 `resolveImageURI` 调用之后、直连创建之前插入 Acquire 尝试）
- Test: `internal/adapter/opensandbox/warmpool_test.go`（追加）

**Interfaces:**
- Consumes: Task 5 的 `lookupPoolByTemplate`、`warmPool.AcquireID`；现有 `a.lifecycle.GetSandbox`（`mockLifecycleClient` 已可 mock）、`a.registry`
- Produces: `CreateSandbox` 命中热池路径（返回的 `*adapter.Sandbox` 字段与直连路径一致）

- [ ] **Step 1: 追加失败测试**

```go
func TestCreateSandbox_PoolHit(t *testing.T) {
	fake := &fakeWarmPool{acquireID: "sbx-warm-1"}
	a := newPoolTestAdapter(fake)
	mock := &mockLifecycleClient{} // 复用包内既有 mock
	mock.getSandboxFn = func(context.Context, string) (*opensandbox.SandboxInfo, error) {
		return &opensandbox.SandboxInfo{
			ID:        "sbx-warm-1",
			CreatedAt: time.Now(),
			Status:    opensandbox.SandboxStatus{State: opensandbox.StateRunning},
		}, nil
	}
	mock.createSandboxFn = func(context.Context, opensandbox.CreateSandboxRequest) (*opensandbox.SandboxInfo, error) {
		t.Error("direct CreateSandbox must not be called on pool hit")
		return nil, nil
	}
	a.lifecycle = mock

	if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-hit",
	}); err != nil {
		t.Fatalf("create pool: %v", err)
	}
	sb, err := a.CreateSandbox(context.Background(), &adapter.CreateSandboxRequest{TemplateID: "base"})
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}
	if sb.SandboxID != "sbx-warm-1" || sb.Backend != "opensandbox" || sb.TemplateID != "base" {
		t.Errorf("CreateSandbox() = %+v, want sbx-warm-1", sb)
	}
}

func TestCreateSandbox_PoolAcquireFails_FallsBack(t *testing.T) {
	fake := &fakeWarmPool{acquireErr: errors.New("pool exhausted")}
	a := newPoolTestAdapter(fake)
	mock := &mockLifecycleClient{}
	mock.createSandboxFn = func(context.Context, opensandbox.CreateSandboxRequest) (*opensandbox.SandboxInfo, error) {
		return &opensandbox.SandboxInfo{
			ID:        "sbx-cold-1",
			CreatedAt: time.Now(),
			Status:    opensandbox.SandboxStatus{State: opensandbox.StateRunning},
		}, nil
	}
	a.lifecycle = mock

	if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-miss",
	}); err != nil {
		t.Fatalf("create pool: %v", err)
	}
	sb, err := a.CreateSandbox(context.Background(), &adapter.CreateSandboxRequest{TemplateID: "base"})
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}
	if sb.SandboxID != "sbx-cold-1" {
		t.Errorf("CreateSandbox() = %q, want fallback sbx-cold-1", sb.SandboxID)
	}
}

func TestCreateSandbox_NoPool_Unchanged(t *testing.T) {
	a := newPoolTestAdapter(&fakeWarmPool{})
	mock := &mockLifecycleClient{}
	mock.createSandboxFn = func(context.Context, opensandbox.CreateSandboxRequest) (*opensandbox.SandboxInfo, error) {
		return &opensandbox.SandboxInfo{
			ID:        "sbx-direct",
			CreatedAt: time.Now(),
			Status:    opensandbox.SandboxStatus{State: opensandbox.StateRunning},
		}, nil
	}
	a.lifecycle = mock
	sb, err := a.CreateSandbox(context.Background(), &adapter.CreateSandboxRequest{TemplateID: "base"})
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}
	if sb.SandboxID != "sbx-direct" {
		t.Errorf("CreateSandbox() = %q, want sbx-direct", sb.SandboxID)
	}
}
```

**注意**：`mockLifecycleClient`（`adapter_test.go:425`）的既有字段名/注入方式以实际代码为准——若它用固定返回值而非函数字段，先给该 mock 增加 `getSandboxFn`/`createSandboxFn` 函数字段（保持既有测试行为为默认值），这属于本 Task 的第一步改动。`SandboxInfo.Status` 的类型名（`SandboxStatus`）同样以 SDK 实际定义为准（`CreateSandbox` 现有代码用 `sbx.Status.State` 与 `opensandbox.StateRunning`，对齐即可）。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/adapter/opensandbox/ -run 'TestCreateSandbox_Pool' -v`
Expected: `TestCreateSandbox_PoolHit` FAIL（当前直连创建，返回的不是 sbx-warm-1 或 mock 报错被触发）

- [ ] **Step 3: 实现** — `CreateSandbox` 中，`resolveImageURI` 之后、`image := &opensandbox.ImageSpec{...}` 之前插入：

```go
	// Prefer a warm pool sandbox when a pool serves this template. Acquire
	// returns a ready sandbox (the pool handles ready-wait and health
	// checks), so skip waitRunning on this path. On any failure fall through
	// to direct creation.
	if entry := a.lookupPoolByTemplate(req.TemplateID); entry != nil {
		sandboxID, acqErr := entry.pool.AcquireID(ctx)
		if acqErr == nil {
			startedAt := time.Now()
			if info, infoErr := a.lifecycle.GetSandbox(ctx, sandboxID); infoErr == nil {
				startedAt = info.CreatedAt
			}
			if a.registry != nil {
				a.registry.SandboxBackend().Set(sandboxID, a.name)
			}
			return &adapter.Sandbox{
				SandboxID:  sandboxID,
				TemplateID: req.TemplateID,
				Status:     adapter.StatusRunning,
				StartedAt:  startedAt,
				Metadata:   req.Metadata,
				Backend:    a.name,
			}, nil
		}
		// Pool miss/exhausted: fall back to direct creation below.
	}
```

`Status` 字段取值对齐直连路径的 `mapState(opensandbox.StateRunning)` 返回值——实现时查看 `mapState` 对 `StateRunning` 的映射（如 `"running"`），用同一常量/字面量，勿凭空造 `adapter.StatusRunning`（若包内无此常量则直接写 `mapState(opensandbox.StateRunning)`）。
回落路径打一条 warn 日志：若包内已有 logger 模式则沿用；否则用 `log/slog`：`slog.Warn("warm pool acquire failed, falling back to direct create", "adapter", a.name, "templateID", req.TemplateID, "error", acqErr)`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test -race ./internal/adapter/opensandbox/ -v`
Expected: 全部 PASS（含既有 CreateSandbox 测试无回归）

- [ ] **Step 5: 提交**

```bash
git add internal/adapter/opensandbox/
git commit -m "feat(opensandbox): serve CreateSandbox from warm pools with direct-create fallback"
```

---

### Task 7: 文档 + 全量验证

**Files:**
- Modify: `AGENTS.md`（warm pool 章节：两后端语义差异表）
- Modify: `configs/e2bgateway-example.yaml`（opensandbox `pool` 段、agentsandbox `warmPoolUpdateStrategy`/`maxWarmPoolSize`/`templateToSandboxTemplate`）
- Modify: `docs/api/openapi.yaml`（warm-pools 端点描述更新为对 agentsandbox/opensandbox 可用；如该文件无 per-backend 描述则跳过）
- Modify: `.github` 无；issue #32 不关闭（PR merge 时自动关联）

**Interfaces:**
- Consumes: Task 1-6 全部产物
- Produces: 可提 PR 的完整变更

- [ ] **Step 1: AGENTS.md** — 在 adapter 相关章节后新增小节（位置：紧跟现有 SandboxAdapter Interface 章节之后）：

```markdown
### Warm Pool Semantics per Backend

| | agent-sandbox | opensandbox | e2b-cloud |
|---|---|---|---|
| Model | Server-side `SandboxWarmPool` CRD (controller-reconciled) | In-process SDK pool registry (`DefaultSandboxPool`) | E2B cloud API |
| Survives gateway restart | Yes (declarative) | No (pool definitions are in-process; Redis state store only preserves idle sandbox records) | Yes |
| CreateSandbox integration | Claim references `WarmPoolRef` (config `warmPoolName` / `templateToWarmPool`, or pools created via CRUD) | `pool.Acquire` when a pool serves the template; falls back to direct create | Cloud-managed |
| TargetSize cap | `maxWarmPoolSize` (default 256) | `pool.maxWarmPoolSize` (default 256) | — |

Warm pool CRUD: `GET/POST /api/v1/warm-pools`, `GET/DELETE /api/v1/warm-pools/{id}`, `POST /api/v1/warm-pools/{id}/size`. Shared ID rules in `internal/adapter/warmpool.go` (K8s-name compatible, ≤63 chars).
```

- [ ] **Step 2: configs/e2bgateway-example.yaml** — 找到 opensandbox 与 agentsandbox 后端示例段，追加（保持文件既有缩进风格；先读文件再改）：

```yaml
      # Warm Pool CRUD (opensandbox): in-process SDK pools.
      pool:
        maxWarmPoolSize: 256
        stateStore:
          type: memory        # memory (default) | redis
          # addr: localhost:6379
          # password: ""
          # db: 0
          # keyPrefix: "opensandbox:pool"
```

```yaml
      # Warm Pool CRUD (agentsandbox): SandboxWarmPool CRDs.
      warmPoolUpdateStrategy: OnReplenish   # OnReplenish (default) | Recreate
      maxWarmPoolSize: 256
      # templateToSandboxTemplate:
      #   my-e2b-template: my-sandbox-template
```

- [ ] **Step 3: docs/api/openapi.yaml** — 检查 `/warm-pools` 路径描述；若有 backend 支持说明则更新，无则跳过（不新增结构）。

- [ ] **Step 4: 全量验证**

Run: `make pre-commit`
Expected: fmt / vet / lint / test 全绿。若 golangci-lint 报 `gocyclo`（`CreateSandbox` 因插入 Acquire 块变长），把 Acquire 块提取为 `func (a *Adapter) tryAcquireFromPool(ctx context.Context, req *adapter.CreateSandboxRequest) (*adapter.Sandbox, bool)` 私有方法后复跑。

Run: `go build ./... && go test -race ./...`
Expected: 全仓通过（e2bcloud/mock 无回归）

- [ ] **Step 5: 提交**

```bash
git add AGENTS.md configs/e2bgateway-example.yaml docs/api/openapi.yaml
git commit -m "docs: document warm pool CRUD semantics for agent-sandbox and opensandbox"
```

- [ ] **Step 6: 收尾** — 走 `submit-pr` skill（`.agents/skills/submit-pr/SKILL.md`）开 PR，PR body 链接 spec 文件与 issue #32。

---

## Self-Review 记录

1. **Spec coverage**：§3.2 agentsandbox 五方法+Status 推导+TemplateRef 校验+warmPoolMap 回写 → Task 2/3；§3.2 opensandbox 注册表+StateStore+Acquire 接入+生命周期注记 → Task 4/5/6（无 Close：进程生命周期池，`defaultNewPool` 已设 `TimeoutSeconds` 兜底回收）；§5 配置双变体 → Task 2 Step 3b / Task 4 Step 3c；§6 安全（ID 校验、clamp 256、CreationSpec 仅服务端来源）→ Task 1/2/5；§7 可观测（回落 warn 日志）→ Task 6；§8 测试策略 → 各 Task Step 1 + Task 7 Step 4。spec §4 handler 409/400 注记为不改 HTTP 层，与 Global Constraints 一致。
2. **Placeholder scan**：Task 6 Step 1 对 `mockLifecycleClient` 字段名与 `SandboxInfo.Status` 类型名有"以实际代码为准"的显式核对指令（mock 是既有代码，plan 中不复制其全文），其余步骤均为完整代码。
3. **Type consistency**：`ClampTargetSize(n, max) (int, error)`、`GenerateWarmPoolID(templateID) string`、`warmPool.AcquireID(ctx) (string, error)`、`newPoolFn(ctx, poolID, imageURI string, size int) (warmPool, error)`、`poolEntry{pool, templateID, targetSize, createdAt}`、`lookupPoolByTemplate(templateID) *poolEntry` 在 Task 1/4/5/6 间签名一致；agentsandbox 测试辅助 `newWarmPoolTestAdapter` 由 Task 2 定义、Task 3 复用。

## Checklist（执行完成后逐项核对）

- [ ] Task 1-7 全部提交，`git log` 干净
- [ ] `go build ./...` 通过（四 adapter 均实现接口——agentsandbox/opensandbox 本次补齐，e2bcloud/mock 未动）
- [ ] `go test -race ./...` 通过
- [ ] `make pre-commit` 全绿
- [ ] `AGENTS.md` / `configs/e2bgateway-example.yaml` 已更新
- [ ] PR body 链接 spec + issue #32（submit-pr skill）
