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
