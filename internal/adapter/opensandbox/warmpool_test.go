// Copyright The E2BGateway Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package opensandbox

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
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

func TestResolveImageURI_TemplateStore(t *testing.T) {
	store := NewMemoryTemplateStore()
	a := &Adapter{templateToImage: map[string]string{}, templates: store}
	// Unknown template falls back to the template ID itself.
	got, err := a.resolveImageURI(context.Background(), "unknown")
	if err != nil {
		t.Fatalf("resolveImageURI() error = %v", err)
	}
	if got != "unknown" {
		t.Errorf("resolveImageURI() = %q, want unknown", got)
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
		{"not started", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleNotStarted}, "pending"},
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

func TestBuildPoolStateStore_ExplicitMemory(t *testing.T) {
	a := &Adapter{poolStoreCfg: &PoolStateStoreConfig{Type: "memory"}}
	store, err := a.buildPoolStateStore()
	if err != nil {
		t.Fatalf("buildPoolStateStore() error = %v", err)
	}
	if store != nil {
		t.Error("buildPoolStateStore(memory) = non-nil, want nil (SDK memory default)")
	}
}

func TestBuildPoolStateStore_RedisRequiresAddr(t *testing.T) {
	a := &Adapter{poolStoreCfg: &PoolStateStoreConfig{Type: "redis"}}
	if _, err := a.buildPoolStateStore(); err == nil {
		t.Error("buildPoolStateStore() redis without addr = nil error")
	}
}

func TestBuildPoolStateStore_UnknownType(t *testing.T) {
	a := &Adapter{poolStoreCfg: &PoolStateStoreConfig{Type: "etcd"}}
	if _, err := a.buildPoolStateStore(); err == nil {
		t.Error("buildPoolStateStore(etcd) = nil error, want unknown-type error")
	}
}

// fakeWarmPool implements warmPool for tests.
type fakeWarmPool struct {
	startErr   error
	acquireID  string
	acquireErr error
	snap       *opensandbox.PoolSnapshot
	snapErr    error
	resizeErr  error
	releaseErr error

	mu         sync.Mutex
	resizedTo  int
	lastResize int
	shutdowns  int
	releases   int
	calls      []string
}

func (f *fakeWarmPool) Start(context.Context) error { return f.startErr }

func (f *fakeWarmPool) AcquireID(context.Context) (string, error) {
	return f.acquireID, f.acquireErr
}

func (f *fakeWarmPool) Resize(_ context.Context, n int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizedTo = n
	f.lastResize = n
	f.calls = append(f.calls, "resize")
	return f.resizeErr
}

func (f *fakeWarmPool) Snapshot(context.Context) (*opensandbox.PoolSnapshot, error) {
	if f.snapErr != nil {
		return nil, f.snapErr
	}
	if f.snap == nil {
		return &opensandbox.PoolSnapshot{LifecycleState: opensandbox.PoolLifecycleRunning}, nil
	}
	return f.snap, nil
}

func (f *fakeWarmPool) Shutdown(context.Context, bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shutdowns++
	f.calls = append(f.calls, "shutdown")
	return nil
}

func (f *fakeWarmPool) ReleaseAllIdle(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	f.calls = append(f.calls, "release")
	return 0, f.releaseErr
}

func newPoolTestAdapter(fake *fakeWarmPool) *Adapter {
	a := &Adapter{
		name:            "opensandbox",
		baseURL:         "http://localhost:8080",
		templateToImage: map[string]string{"base": "registry/base:v1"},
		pools:           make(map[string]*poolEntry),
		poolStarting:    make(map[string]struct{}),
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

func TestCreateWarmPool_InvalidInput(t *testing.T) {
	a := newPoolTestAdapter(&fakeWarmPool{})
	if _, err := a.CreateWarmPool(context.Background(), nil); err == nil {
		t.Error("CreateWarmPool(nil) = nil, want error")
	}
	if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{TargetSize: 1}); err == nil {
		t.Error("CreateWarmPool() without templateID = nil, want error")
	}
	if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: -1,
	}); err == nil {
		t.Error("CreateWarmPool(-1) = nil, want error")
	}
	if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "Bad ID",
	}); err == nil {
		t.Error("CreateWarmPool(bad id) = nil, want error")
	}
}

func TestCreateWarmPool_GeneratesValidID(t *testing.T) {
	a := newPoolTestAdapter(&fakeWarmPool{})
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

func TestCreateWarmPool_ClampsSize(t *testing.T) {
	a := newPoolTestAdapter(&fakeWarmPool{})
	wp, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 100000, WarmPoolID: "wp-clamp",
	})
	if err != nil {
		t.Fatalf("CreateWarmPool() error = %v", err)
	}
	if wp.TargetSize != 256 {
		t.Errorf("TargetSize = %d, want 256", wp.TargetSize)
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
	if wp.Status != adapter.WarmPoolStatusScaling || wp.CurrentSize != 2 || wp.TargetSize != 3 {
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

func TestListWarmPools_Empty(t *testing.T) {
	a := newPoolTestAdapter(&fakeWarmPool{})
	pools, err := a.ListWarmPools(context.Background())
	if err != nil {
		t.Fatalf("ListWarmPools() error = %v", err)
	}
	if pools == nil || len(pools) != 0 {
		t.Errorf("ListWarmPools() = %v, want empty non-nil slice", pools)
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
	// Fresh fake per pool: a shared instance would race on its own counters
	// (the registry, not the fake, is what this test exercises).
	a.newPoolFn = func(context.Context, string, string, int) (warmPool, error) {
		return &fakeWarmPool{}, nil
	}
	done := make(chan struct{})
	for i := range 8 {
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
	for range 8 {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent access timed out")
		}
	}
}

func TestCreateSandbox_PoolHit(t *testing.T) {
	fake := &fakeWarmPool{acquireID: "sbx-warm-1"}
	a := newPoolTestAdapter(fake)
	fakeLifecycle := &fakeLifecycleClient{}
	a.lifecycle = fakeLifecycle

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
		t.Errorf("CreateSandbox() = %+v, want sbx-warm-1 from pool", sb)
	}
	if len(fakeLifecycle.createRequests) != 0 {
		t.Errorf("direct CreateSandbox called %d times on pool hit, want 0", len(fakeLifecycle.createRequests))
	}
}

func TestCreateSandbox_PoolAcquireFails_FallsBack(t *testing.T) {
	fake := &fakeWarmPool{acquireErr: errors.New("pool exhausted")}
	a := newPoolTestAdapter(fake)
	fakeLifecycle := &fakeLifecycleClient{}
	a.lifecycle = fakeLifecycle

	if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-miss",
	}); err != nil {
		t.Fatalf("create pool: %v", err)
	}
	sb, err := a.CreateSandbox(context.Background(), &adapter.CreateSandboxRequest{TemplateID: "base"})
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}
	if len(fakeLifecycle.createRequests) != 1 {
		t.Fatalf("direct create calls = %d, want 1 (fallback)", len(fakeLifecycle.createRequests))
	}
	if sb.SandboxID == "" || sb.SandboxID == "sbx-warm-1" {
		t.Errorf("CreateSandbox() = %q, want fallback sandbox ID", sb.SandboxID)
	}
}

func TestCreateSandbox_NoPool_Unchanged(t *testing.T) {
	a := newPoolTestAdapter(&fakeWarmPool{})
	fakeLifecycle := &fakeLifecycleClient{}
	a.lifecycle = fakeLifecycle

	sb, err := a.CreateSandbox(context.Background(), &adapter.CreateSandboxRequest{TemplateID: "base"})
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}
	if len(fakeLifecycle.createRequests) != 1 {
		t.Errorf("direct create calls = %d, want 1", len(fakeLifecycle.createRequests))
	}
	// The pool creation spec must use the resolved image, never request data.
	if got := fakeLifecycle.createRequests[0].Image.URI; got != "registry/base:v1" {
		t.Errorf("image URI = %q, want registry/base:v1", got)
	}
	if sb.SandboxID == "" {
		t.Error("CreateSandbox() returned empty sandbox ID")
	}
}

func TestCreateSandbox_OtherTemplate_Unaffected(t *testing.T) {
	fake := &fakeWarmPool{acquireID: "sbx-warm-1"}
	a := newPoolTestAdapter(fake)
	fakeLifecycle := &fakeLifecycleClient{}
	a.lifecycle = fakeLifecycle
	a.templateToImage["other"] = "registry/other:v1"

	if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-only-base",
	}); err != nil {
		t.Fatalf("create pool: %v", err)
	}
	// A request for a different template must not hit the "base" pool.
	sb, err := a.CreateSandbox(context.Background(), &adapter.CreateSandboxRequest{TemplateID: "other"})
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}
	if len(fakeLifecycle.createRequests) != 1 {
		t.Errorf("direct create calls = %d, want 1", len(fakeLifecycle.createRequests))
	}
	if sb.SandboxID == "sbx-warm-1" {
		t.Error("pool for template base served request for template other")
	}
}

// --- Review remediation tests (PR #82) ---

// TestDefaultNewPool_Builds exercises the PRODUCTION pool factory (no fake)
// against the real SDK builder. A previous revision set
// CreationSpec.TimeoutSeconds, which SDK v1.2.0 Build() rejects — every
// production CreateWarmPool failed while fake-backed tests stayed green.
func TestDefaultNewPool_Builds(t *testing.T) {
	a := &Adapter{name: "test", baseURL: "http://localhost:8080", apiKey: "k"}
	p, err := a.defaultNewPool(context.Background(), "wp-build", "registry/img:v1", 2)
	if err != nil {
		t.Fatalf("defaultNewPool() error = %v", err)
	}
	if p == nil {
		t.Fatal("defaultNewPool() = nil pool")
	}
}

func TestDefaultNewPool_BuildsWithRedisStoreConfig(t *testing.T) {
	a := &Adapter{
		name:    "test",
		baseURL: "http://localhost:8080",
		apiKey:  "k",
		// Client creation is lazy; no server needed for Build().
		poolStoreCfg: &PoolStateStoreConfig{Type: "redis", Addr: "127.0.0.1:6379", KeyPrefix: "test:pool"},
	}
	if _, err := a.defaultNewPool(context.Background(), "wp-build-redis", "registry/img:v1", 1); err != nil {
		t.Fatalf("defaultNewPool(redis) error = %v", err)
	}
}

func TestDefaultNewPool_BuildsWithIdleTimeout(t *testing.T) {
	a := &Adapter{name: "test", baseURL: "http://localhost:8080", apiKey: "k", poolIdleTimeout: time.Hour}
	if _, err := a.defaultNewPool(context.Background(), "wp-build-ttl", "registry/img:v1", 1); err != nil {
		t.Fatalf("defaultNewPool(idleTimeout) error = %v", err)
	}
}

func TestCreateWarmPool_UnmappedTemplateRejected(t *testing.T) {
	a := newPoolTestAdapter(&fakeWarmPool{})
	a.templateToImage = map[string]string{} // nothing mapped
	a.templates = nil
	// Unlike CreateSandbox, pool creation must NOT fall back to using the raw
	// template ID as the container image: pools pre-warm long-lived sandboxes,
	// so the image must come from trusted server-side configuration.
	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "alpine:latest", TargetSize: 1, WarmPoolID: "wp-strict",
	})
	if err == nil {
		t.Fatal("CreateWarmPool(unmapped template) = nil, want rejection")
	}
	if _, ok := a.pools["wp-strict"]; ok {
		t.Error("rejected pool must not be registered")
	}
}

func TestCreateWarmPool_TemplateStoreResolution(t *testing.T) {
	a := newPoolTestAdapter(&fakeWarmPool{})
	a.templateToImage = map[string]string{}
	store := NewMemoryTemplateStore()
	a.templates = store
	ctx := context.Background()
	if err := store.CreateTemplate(ctx, &TemplateEntry{
		TemplateID: "stored-tmpl",
		ImageURI:   "registry/stored:v1",
	}); err != nil {
		t.Fatalf("seeding template store: %v", err)
	}
	var gotImage string
	a.newPoolFn = func(_ context.Context, _, imageURI string, _ int) (warmPool, error) {
		gotImage = imageURI
		return &fakeWarmPool{}, nil
	}
	if _, err := a.CreateWarmPool(ctx, &adapter.WarmPoolCreateRequest{
		TemplateID: "stored-tmpl", TargetSize: 1, WarmPoolID: "wp-stored",
	}); err != nil {
		t.Fatalf("CreateWarmPool() error = %v", err)
	}
	if gotImage != "registry/stored:v1" {
		t.Errorf("pool image = %q, want registry/stored:v1", gotImage)
	}
}

func TestCreateWarmPool_LowercaseImageFallback(t *testing.T) {
	a := newPoolTestAdapter(&fakeWarmPool{})
	// Viper lowercases config map keys.
	a.templateToImage = map[string]string{"mytemplate": "registry/my:v1"}
	var gotImage string
	a.newPoolFn = func(_ context.Context, _, imageURI string, _ int) (warmPool, error) {
		gotImage = imageURI
		return &fakeWarmPool{}, nil
	}
	if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "MyTemplate", TargetSize: 1, WarmPoolID: "wp-lower",
	}); err != nil {
		t.Fatalf("CreateWarmPool() error = %v", err)
	}
	if gotImage != "registry/my:v1" {
		t.Errorf("pool image = %q, want registry/my:v1 (lowercased key fallback)", gotImage)
	}
}

func TestCreateWarmPool_SnapshotFailureRollsBack(t *testing.T) {
	fake := &fakeWarmPool{snapErr: errors.New("snap boom")}
	a := newPoolTestAdapter(fake)
	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-snap",
	})
	if err == nil {
		t.Fatal("CreateWarmPool() = nil, want snapshot error")
	}
	if _, ok := a.pools["wp-snap"]; ok {
		t.Error("failed create must not leave a registered pool (retry would hit AlreadyExists)")
	}
	fake.mu.Lock()
	shutdowns := fake.shutdowns
	fake.mu.Unlock()
	if shutdowns != 1 {
		t.Errorf("rollback shutdowns = %d, want 1", shutdowns)
	}
}

func TestCreateWarmPool_ConcurrentSameID(t *testing.T) {
	a := newPoolTestAdapter(&fakeWarmPool{})
	var built atomic.Int32
	a.newPoolFn = func(context.Context, string, string, int) (warmPool, error) {
		built.Add(1)
		// Overlap concurrent attempts: the ID must already be reserved here.
		time.Sleep(10 * time.Millisecond)
		return &fakeWarmPool{}, nil
	}
	var okCount atomic.Int32
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
				TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-once",
			})
			switch {
			case err == nil:
				okCount.Add(1)
			case errors.Is(err, adapter.ErrWarmPoolAlreadyExists):
			default:
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
	wg.Wait()
	if okCount.Load() != 1 {
		t.Errorf("successful creates = %d, want exactly 1", okCount.Load())
	}
	if n := built.Load(); n != 1 {
		t.Errorf("pool factory invoked %d times, want 1 (ID must be reserved before build/start)", n)
	}
	a.poolsMu.RLock()
	n := len(a.pools)
	a.poolsMu.RUnlock()
	if n != 1 {
		t.Errorf("registered pools = %d, want 1 (no hidden running pool)", n)
	}
}

func TestDeleteWarmPool_ReleasesIdleAfterShutdown(t *testing.T) {
	fake := &fakeWarmPool{}
	a := newPoolTestAdapter(fake)
	if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-drain",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := a.DeleteWarmPool(context.Background(), "wp-drain"); err != nil {
		t.Fatalf("DeleteWarmPool() error = %v", err)
	}
	fake.mu.Lock()
	calls := append([]string{}, fake.calls...)
	releases := fake.releases
	fake.mu.Unlock()
	if releases != 1 {
		t.Errorf("ReleaseAllIdle calls = %d, want 1 (idle sandboxes must not outlive the pool)", releases)
	}
	// Shutdown must come first so the reconciler cannot replenish released slots.
	shutdownIdx, releaseIdx := -1, -1
	for i, c := range calls {
		if c == "shutdown" && shutdownIdx == -1 {
			shutdownIdx = i
		}
		if c == "release" && releaseIdx == -1 {
			releaseIdx = i
		}
	}
	if shutdownIdx == -1 || releaseIdx == -1 || shutdownIdx > releaseIdx {
		t.Errorf("call order = %v, want shutdown before release", calls)
	}
}

func TestDeleteWarmPool_ReleaseError(t *testing.T) {
	fake := &fakeWarmPool{releaseErr: errors.New("release boom")}
	a := newPoolTestAdapter(fake)
	if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-relerr",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := a.DeleteWarmPool(context.Background(), "wp-relerr"); err == nil {
		t.Error("DeleteWarmPool() = nil, want release error surfaced")
	}
	// The pool is still unregistered: it is shut down and must not be routed to.
	if _, ok := a.pools["wp-relerr"]; ok {
		t.Error("pool must be unregistered even when release fails")
	}
}

func TestUpdateWarmPoolSize_ConcurrentConsistency(t *testing.T) {
	fake := &fakeWarmPool{}
	a := newPoolTestAdapter(fake)
	if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-consistent",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	var wg sync.WaitGroup
	for _, size := range []int{7, 3, 5} {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			_ = a.UpdateWarmPoolSize(context.Background(), "wp-consistent", s)
		}(size)
	}
	wg.Wait()

	a.poolsMu.RLock()
	entry := a.pools["wp-consistent"]
	a.poolsMu.RUnlock()
	if entry == nil {
		t.Fatal("entry missing")
	}
	entry.opMu.Lock()
	got := entry.targetSize
	entry.opMu.Unlock()
	fake.mu.Lock()
	last := fake.lastResize
	fake.mu.Unlock()
	// The recorded size must match the LAST completed SDK resize — no
	// interleaving where the entry reports a size the SDK never ended on.
	if got != last {
		t.Errorf("entry.targetSize = %d but last SDK Resize = %d (must be consistent)", got, last)
	}
}

func TestCreateSandbox_WithEnvs_BypassesPool(t *testing.T) {
	fake := &fakeWarmPool{acquireID: "sbx-warm-1"}
	a := newPoolTestAdapter(fake)
	fakeLifecycle := &fakeLifecycleClient{}
	a.lifecycle = fakeLifecycle

	if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-env",
	}); err != nil {
		t.Fatalf("create pool: %v", err)
	}
	// Pool sandboxes are pre-created from the pool's fixed CreationSpec and
	// cannot carry per-request envs — such requests must go direct.
	sb, err := a.CreateSandbox(context.Background(), &adapter.CreateSandboxRequest{
		TemplateID: "base",
		Envs:       map[string]string{"FOO": "bar"},
	})
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}
	if len(fakeLifecycle.createRequests) != 1 {
		t.Errorf("direct create calls = %d, want 1 (envs must bypass the pool)", len(fakeLifecycle.createRequests))
	}
	if sb.SandboxID == "sbx-warm-1" {
		t.Error("request with envs was served from the pool; envs would be silently dropped")
	}
	if got := fakeLifecycle.createRequests[0].Env["FOO"]; got != "bar" {
		t.Errorf("direct create Env[FOO] = %q, want bar", got)
	}
}

func TestCreateSandbox_WithMetadata_BypassesPool(t *testing.T) {
	fake := &fakeWarmPool{acquireID: "sbx-warm-1"}
	a := newPoolTestAdapter(fake)
	fakeLifecycle := &fakeLifecycleClient{}
	a.lifecycle = fakeLifecycle

	if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-meta",
	}); err != nil {
		t.Fatalf("create pool: %v", err)
	}
	sb, err := a.CreateSandbox(context.Background(), &adapter.CreateSandboxRequest{
		TemplateID: "base",
		Metadata:   map[string]string{"owner": "alice"},
	})
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}
	if len(fakeLifecycle.createRequests) != 1 {
		t.Errorf("direct create calls = %d, want 1 (metadata must bypass the pool)", len(fakeLifecycle.createRequests))
	}
	if sb.SandboxID == "sbx-warm-1" {
		t.Error("request with metadata was served from the pool; metadata would be silently dropped")
	}
}
