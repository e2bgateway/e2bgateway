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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
)

func TestResolveImageURI(t *testing.T) {
	tests := []struct {
		name       string
		mapping    map[string]string
		templateID string
		want       string
	}{
		{
			name:       "static map exact match",
			mapping:    map[string]string{"base": "registry.example.com/base:v1"},
			templateID: "base",
			want:       "registry.example.com/base:v1",
		},
		{
			name:       "lowercased key fallback (Viper lowercases config map keys)",
			mapping:    map[string]string{"mytemplate": "registry.example.com/my:v1"},
			templateID: "MyTemplate",
			want:       "registry.example.com/my:v1",
		},
		{
			name:       "unmapped falls back to the template ID as image URI",
			mapping:    map[string]string{},
			templateID: "some/image:latest",
			want:       "some/image:latest",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Adapter{templateToImage: tt.mapping}
			got, err := a.resolveImageURI(context.Background(), tt.templateID)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveImageURI_TemplateStore(t *testing.T) {
	store := NewMemoryTemplateStore()
	a := &Adapter{templateToImage: map[string]string{}, templates: store}
	// Unknown template falls back to the template ID itself.
	got, err := a.resolveImageURI(context.Background(), "unknown")
	require.NoError(t, err)
	assert.Equal(t, "unknown", got)
}

func TestPoolConnectionConfig(t *testing.T) {
	a := &Adapter{baseURL: "https://sandbox.example.com:8443", apiKey: "k"}
	cc, err := a.poolConnectionConfig()
	require.NoError(t, err)
	assert.Equal(t, "https", cc.Protocol)
	assert.Equal(t, "sandbox.example.com:8443", cc.Domain)
	assert.Equal(t, "k", cc.APIKey)
}

func TestPoolConnectionConfig_Invalid(t *testing.T) {
	a := &Adapter{baseURL: "not a url"}
	_, err := a.poolConnectionConfig()
	assert.Error(t, err, "want parse error")
}

func TestPoolStatus_Mapping(t *testing.T) {
	tests := []struct {
		name string
		snap *opensandbox.PoolSnapshot
		want string
	}{
		{"running full", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleRunning, IdleCount: 3, MaxIdle: 3}, adapter.WarmPoolStatusReady},
		{"running filling", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleRunning, IdleCount: 1, MaxIdle: 3}, adapter.WarmPoolStatusScaling},
		{"running degraded", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleRunning, HealthState: opensandbox.PoolDegraded,
			IdleCount: 3, MaxIdle: 3}, adapter.WarmPoolStatusDegraded},
		{"starting", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleStarting}, adapter.WarmPoolStatusPending},
		{"not started", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleNotStarted}, adapter.WarmPoolStatusPending},
		{"stopped", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleStopped}, adapter.WarmPoolStatusDestroyed},
		{"draining", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleDraining}, adapter.WarmPoolStatusDestroyed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, poolStatus(tt.snap))
		})
	}
}

func TestBuildPoolStateStore(t *testing.T) {
	t.Run("default is SDK memory", func(t *testing.T) {
		a := &Adapter{}
		store, err := a.buildPoolStateStore("registry/base:v1")
		require.NoError(t, err)
		// Explicit (not nil): the pool and its PoolManager must share ONE
		// store instance, so the manager's fence/tombstone is visible to the
		// pool's reconcile loop.
		assert.NotNil(t, store, "memory config must yield an explicit in-memory store")
	})
	t.Run("explicit memory", func(t *testing.T) {
		a := &Adapter{poolStoreCfg: &PoolStateStoreConfig{Type: "memory"}}
		store, err := a.buildPoolStateStore("registry/base:v1")
		require.NoError(t, err)
		assert.NotNil(t, store)
	})
	t.Run("redis requires addr", func(t *testing.T) {
		a := &Adapter{poolStoreCfg: &PoolStateStoreConfig{Type: "redis"}}
		_, err := a.buildPoolStateStore("registry/base:v1")
		assert.Error(t, err)
	})
	t.Run("unknown type", func(t *testing.T) {
		a := &Adapter{poolStoreCfg: &PoolStateStoreConfig{Type: "etcd"}}
		_, err := a.buildPoolStateStore("registry/base:v1")
		assert.Error(t, err)
	})
	t.Run("redis password without tls to remote addr is rejected", func(t *testing.T) {
		a := &Adapter{poolStoreCfg: &PoolStateStoreConfig{
			Type: "redis", Addr: "redis.example.com:6379", Password: "secret",
		}}
		_, err := a.buildPoolStateStore("registry/base:v1")
		assert.ErrorContains(t, err, "cleartext")
	})
	t.Run("redis password with tls to remote addr is accepted", func(t *testing.T) {
		a := &Adapter{poolStoreCfg: &PoolStateStoreConfig{
			Type: "redis", Addr: "redis.example.com:6379", Password: "secret", EnableTLS: true,
		}}
		store, err := a.buildPoolStateStore("registry/base:v1")
		require.NoError(t, err)
		assert.NotNil(t, store)
	})
	t.Run("rediss scheme enables tls implicitly", func(t *testing.T) {
		a := &Adapter{poolStoreCfg: &PoolStateStoreConfig{
			Type: "redis", Addr: "rediss://redis.example.com:6379", Password: "secret",
		}}
		store, err := a.buildPoolStateStore("registry/base:v1")
		require.NoError(t, err)
		assert.NotNil(t, store)
	})
}

// fakeWarmPool implements warmPool for tests.
type fakeWarmPool struct {
	startErr    error
	acquireID   string
	acquireErr  error
	snap        *opensandbox.PoolSnapshot
	snapErr     error
	resizeErr   error
	releaseErr  error
	shutdownErr error

	// shutdownGate, when non-nil, blocks Shutdown until closed;
	// enteredShutdown, when non-nil, receives a token when Shutdown begins.
	// Together they let tests observe the window where Delete is mid-teardown.
	shutdownGate    chan struct{}
	enteredShutdown chan struct{}

	mu            sync.Mutex
	resizedTo     int
	lastResize    int
	shutdowns     int
	releases      int
	calls         []string
	gracefulFlags []bool
}

func (f *fakeWarmPool) shutdownGracefulFlags() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.gracefulFlags...)
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

func (f *fakeWarmPool) Shutdown(_ context.Context, graceful bool) error {
	if f.enteredShutdown != nil {
		f.enteredShutdown <- struct{}{}
	}
	if f.shutdownGate != nil {
		<-f.shutdownGate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shutdowns++
	f.gracefulFlags = append(f.gracefulFlags, graceful)
	f.calls = append(f.calls, "shutdown")
	return f.shutdownErr
}

func (f *fakeWarmPool) ReleaseAllIdleParallel(context.Context, int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	f.calls = append(f.calls, "release")
	return 0, f.releaseErr
}

// fakeDestroyer implements poolDestroyer for tests.
type fakeDestroyer struct {
	mu     sync.Mutex
	names  []string
	opts   []opensandbox.PoolDestroyOptions
	err    error
	result *opensandbox.PoolDestroyResult
	block  chan struct{} // when non-nil, Destroy waits on it before returning
}

func (f *fakeDestroyer) Destroy(_ context.Context, name string, opts opensandbox.PoolDestroyOptions) (*opensandbox.PoolDestroyResult, error) {
	f.mu.Lock()
	f.names = append(f.names, name)
	f.opts = append(f.opts, opts)
	block := f.block
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &opensandbox.PoolDestroyResult{PoolName: name, DrainedIdleCount: 2, KilledIdleCount: 2}, nil
}

func (f *fakeDestroyer) snapshot() ([]string, []opensandbox.PoolDestroyOptions) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.names...), append([]opensandbox.PoolDestroyOptions(nil), f.opts...)
}

func newPoolTestAdapter(fake *fakeWarmPool) (*Adapter, *fakeDestroyer) {
	dest := &fakeDestroyer{}
	a := &Adapter{
		name:            "opensandbox",
		baseURL:         "http://localhost:8080",
		templateToImage: map[string]string{"base": "registry/base:v1"},
		pools:           make(map[string]*poolEntry),
		poolStarting:    make(map[string]struct{}),
		maxWarmPoolSize: 256,
	}
	a.newPoolFn = func(context.Context, string, string, int) (warmPool, poolDestroyer, error) {
		return fake, dest, nil
	}
	return a, dest
}

func createTestPool(t *testing.T, a *Adapter, id string) {
	t.Helper()
	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: id,
	})
	require.NoError(t, err, "creating test pool %s", id)
}

func TestCreateWarmPool_Opensandbox(t *testing.T) {
	fake := &fakeWarmPool{}
	a, _ := newPoolTestAdapter(fake)

	wp, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 3, WarmPoolID: "wp-base-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "wp-base-1", wp.WarmPoolID)
	assert.Equal(t, "base", wp.TemplateID)
	assert.Equal(t, 3, wp.TargetSize)

	a.poolsMu.RLock()
	_, registered := a.pools["wp-base-1"]
	a.poolsMu.RUnlock()
	assert.True(t, registered, "pool not registered")
}

func TestCreateWarmPool_InvalidInput(t *testing.T) {
	a, _ := newPoolTestAdapter(&fakeWarmPool{})
	ctx := context.Background()

	_, err := a.CreateWarmPool(ctx, nil)
	assert.Error(t, err, "nil request")
	_, err = a.CreateWarmPool(ctx, &adapter.WarmPoolCreateRequest{TargetSize: 1})
	assert.Error(t, err, "missing templateID")
	_, err = a.CreateWarmPool(ctx, &adapter.WarmPoolCreateRequest{TemplateID: "base", TargetSize: -1})
	assert.Error(t, err, "negative targetSize")
	_, err = a.CreateWarmPool(ctx, &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "Bad ID",
	})
	assert.Error(t, err, "invalid explicit ID")
}

func TestCreateWarmPool_GeneratesValidID(t *testing.T) {
	a, _ := newPoolTestAdapter(&fakeWarmPool{})

	wp, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1,
	})
	require.NoError(t, err)
	assert.NoError(t, adapter.ValidateWarmPoolID(wp.WarmPoolID))
}

func TestCreateWarmPool_Duplicate(t *testing.T) {
	a, _ := newPoolTestAdapter(&fakeWarmPool{})
	req := &adapter.WarmPoolCreateRequest{TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-dup"}
	_, err := a.CreateWarmPool(context.Background(), req)
	require.NoError(t, err, "first create")

	_, err = a.CreateWarmPool(context.Background(), req)
	assert.ErrorIs(t, err, adapter.ErrWarmPoolAlreadyExists)
}

func TestCreateWarmPool_StartError(t *testing.T) {
	a, _ := newPoolTestAdapter(&fakeWarmPool{startErr: errors.New("boom")})

	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-x",
	})
	require.Error(t, err)

	a.poolsMu.RLock()
	_, registered := a.pools["wp-x"]
	a.poolsMu.RUnlock()
	assert.False(t, registered, "failed pool must not be registered")
}

func TestCreateWarmPool_ClampsSize(t *testing.T) {
	a, _ := newPoolTestAdapter(&fakeWarmPool{})

	wp, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 100000, WarmPoolID: "wp-clamp",
	})
	require.NoError(t, err)
	assert.Equal(t, 256, wp.TargetSize)
}

func TestGetWarmPool_Opensandbox(t *testing.T) {
	fake := &fakeWarmPool{snap: &opensandbox.PoolSnapshot{
		LifecycleState: opensandbox.PoolLifecycleRunning, IdleCount: 2, MaxIdle: 3,
	}}
	a, _ := newPoolTestAdapter(fake)
	createTestPool(t, a, "wp-g")
	// The pool was created with TargetSize 1; resize to 3 so the snapshot's
	// MaxIdle matches the entry and status derives to "scaling".
	require.NoError(t, a.UpdateWarmPoolSize(context.Background(), "wp-g", 3))

	wp, err := a.GetWarmPool(context.Background(), "wp-g")
	require.NoError(t, err)
	assert.Equal(t, adapter.WarmPoolStatusScaling, wp.Status)
	assert.Equal(t, 2, wp.CurrentSize)
	assert.Equal(t, 3, wp.TargetSize)
}

func TestGetWarmPool_NotFound(t *testing.T) {
	a, _ := newPoolTestAdapter(&fakeWarmPool{})

	_, err := a.GetWarmPool(context.Background(), "nope")
	assert.ErrorIs(t, err, adapter.ErrWarmPoolNotFound)
}

func TestListWarmPools_Opensandbox(t *testing.T) {
	a, _ := newPoolTestAdapter(&fakeWarmPool{})
	createTestPool(t, a, "wp-1")
	createTestPool(t, a, "wp-2")

	pools, err := a.ListWarmPools(context.Background())
	require.NoError(t, err)
	assert.Len(t, pools, 2)
}

func TestListWarmPools_Empty(t *testing.T) {
	a, _ := newPoolTestAdapter(&fakeWarmPool{})

	pools, err := a.ListWarmPools(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, pools)
	assert.Empty(t, pools)
}

func TestDeleteWarmPool_Opensandbox(t *testing.T) {
	fake := &fakeWarmPool{}
	a, _ := newPoolTestAdapter(fake)
	createTestPool(t, a, "wp-d")

	require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-d", nil))

	fake.mu.Lock()
	shutdowns := fake.shutdowns
	fake.mu.Unlock()
	assert.Equal(t, 1, shutdowns)

	a.poolsMu.RLock()
	_, registered := a.pools["wp-d"]
	a.poolsMu.RUnlock()
	assert.False(t, registered, "pool still registered after delete")

	err := a.DeleteWarmPool(context.Background(), "wp-d", nil)
	assert.ErrorIs(t, err, adapter.ErrWarmPoolNotFound, "second delete")
}

func TestUpdateWarmPoolSize_Opensandbox(t *testing.T) {
	fake := &fakeWarmPool{}
	a, _ := newPoolTestAdapter(fake)
	createTestPool(t, a, "wp-u")

	require.NoError(t, a.UpdateWarmPoolSize(context.Background(), "wp-u", 7))

	fake.mu.Lock()
	resizedTo := fake.resizedTo
	fake.mu.Unlock()
	a.poolsMu.RLock()
	entry := a.pools["wp-u"]
	a.poolsMu.RUnlock()
	require.NotNil(t, entry)
	entry.opMu.Lock()
	targetSize := entry.targetSize
	entry.opMu.Unlock()
	assert.Equal(t, 7, resizedTo)
	assert.Equal(t, 7, targetSize)

	assert.Error(t, a.UpdateWarmPoolSize(context.Background(), "wp-u", -1))
	err := a.UpdateWarmPoolSize(context.Background(), "nope", 1)
	assert.ErrorIs(t, err, adapter.ErrWarmPoolNotFound)
}

func TestLookupPoolByTemplate(t *testing.T) {
	a, _ := newPoolTestAdapter(&fakeWarmPool{})
	createTestPool(t, a, "wp-t")

	assert.NotNil(t, a.lookupPoolByTemplate("base"))
	assert.Nil(t, a.lookupPoolByTemplate("other"))
}

func TestWarmPoolRegistry_ConcurrentAccess(t *testing.T) {
	a, _ := newPoolTestAdapter(&fakeWarmPool{})
	// Fresh fake per pool: a shared instance would race on its own counters
	// (the registry, not the fake, is what this test exercises).
	a.newPoolFn = func(context.Context, string, string, int) (warmPool, poolDestroyer, error) {
		return &fakeWarmPool{}, nil, nil
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
			_ = a.DeleteWarmPool(context.Background(), id, nil)
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

// --- Review remediation tests (PR #82) ---

// TestDefaultNewPool_Builds exercises the PRODUCTION pool factory (no fake)
// against the real SDK builder. A previous revision set
// CreationSpec.TimeoutSeconds, which SDK v1.2.0 Build() rejects — every
// production CreateWarmPool failed while fake-backed tests stayed green.
func TestDefaultNewPool_Builds(t *testing.T) {
	a := &Adapter{name: "test", baseURL: "http://localhost:8080", apiKey: "k"}
	p, _, err := a.defaultNewPool(context.Background(), "wp-build", "registry/img:v1", 2)
	require.NoError(t, err)
	assert.NotNil(t, p)
}

func TestDefaultNewPool_BuildsWithRedisStoreConfig(t *testing.T) {
	a := &Adapter{
		name:    "test",
		baseURL: "http://localhost:8080",
		apiKey:  "k",
		// Client creation is lazy; no server needed for Build().
		poolStoreCfg: &PoolStateStoreConfig{Type: "redis", Addr: "127.0.0.1:6379", KeyPrefix: "test:pool"},
	}
	p, _, err := a.defaultNewPool(context.Background(), "wp-build-redis", "registry/img:v1", 1)
	require.NoError(t, err)
	assert.NotNil(t, p)
}

func TestDefaultNewPool_BuildsWithIdleTimeout(t *testing.T) {
	a := &Adapter{name: "test", baseURL: "http://localhost:8080", apiKey: "k", poolIdleTimeout: time.Hour}
	p, _, err := a.defaultNewPool(context.Background(), "wp-build-ttl", "registry/img:v1", 1)
	require.NoError(t, err)
	assert.NotNil(t, p)
}

func TestCreateWarmPool_UnmappedTemplateRejected(t *testing.T) {
	a, _ := newPoolTestAdapter(&fakeWarmPool{})
	a.templateToImage = map[string]string{} // nothing mapped
	a.templates = nil
	// Unlike CreateSandbox, pool creation must NOT fall back to using the raw
	// template ID as the container image: pools pre-warm long-lived sandboxes,
	// so the image must come from trusted server-side configuration.
	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "alpine:latest", TargetSize: 1, WarmPoolID: "wp-strict",
	})
	require.Error(t, err, "unmapped template must be rejected for pool creation")

	a.poolsMu.RLock()
	_, registered := a.pools["wp-strict"]
	a.poolsMu.RUnlock()
	assert.False(t, registered, "rejected pool must not be registered")
}

func TestCreateWarmPool_TemplateStoreResolution(t *testing.T) {
	a, _ := newPoolTestAdapter(&fakeWarmPool{})
	a.templateToImage = map[string]string{}
	store := NewMemoryTemplateStore()
	a.templates = store
	ctx := context.Background()
	require.NoError(t, store.CreateTemplate(ctx, &TemplateEntry{
		TemplateID: "stored-tmpl",
		ImageURI:   "registry/stored:v1",
	}))

	var gotImage string
	a.newPoolFn = func(_ context.Context, _, imageURI string, _ int) (warmPool, poolDestroyer, error) {
		gotImage = imageURI
		return &fakeWarmPool{}, nil, nil
	}
	_, err := a.CreateWarmPool(ctx, &adapter.WarmPoolCreateRequest{
		TemplateID: "stored-tmpl", TargetSize: 1, WarmPoolID: "wp-stored",
	})
	require.NoError(t, err)
	assert.Equal(t, "registry/stored:v1", gotImage)
}

func TestCreateWarmPool_LowercaseImageFallback(t *testing.T) {
	a, _ := newPoolTestAdapter(&fakeWarmPool{})
	// Viper lowercases config map keys.
	a.templateToImage = map[string]string{"mytemplate": "registry/my:v1"}
	var gotImage string
	a.newPoolFn = func(_ context.Context, _, imageURI string, _ int) (warmPool, poolDestroyer, error) {
		gotImage = imageURI
		return &fakeWarmPool{}, nil, nil
	}

	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "MyTemplate", TargetSize: 1, WarmPoolID: "wp-lower",
	})
	require.NoError(t, err)
	assert.Equal(t, "registry/my:v1", gotImage, "lowercased key fallback")
}

func TestCreateWarmPool_SnapshotFailureRollsBackAndDrains(t *testing.T) {
	fake := &fakeWarmPool{snapErr: errors.New("snap boom")}
	a, _ := newPoolTestAdapter(fake)

	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-snap",
	})
	require.Error(t, err, "want snapshot error")

	a.poolsMu.RLock()
	_, registered := a.pools["wp-snap"]
	a.poolsMu.RUnlock()
	assert.False(t, registered,
		"failed create must not leave a registered pool (retry would hit AlreadyExists)")

	fake.mu.Lock()
	shutdowns, releases := fake.shutdowns, fake.releases
	fake.mu.Unlock()
	assert.Equal(t, 1, shutdowns, "rollback must shut the pool down")
	assert.Equal(t, 1, releases,
		"rollback must drain idle entries so a same-ID retry cannot acquire an orphan")
}

func TestCreateWarmPool_ConcurrentSameID(t *testing.T) {
	a, _ := newPoolTestAdapter(&fakeWarmPool{})
	var built atomic.Int32
	a.newPoolFn = func(context.Context, string, string, int) (warmPool, poolDestroyer, error) {
		built.Add(1)
		// Overlap concurrent attempts: the ID must already be reserved here.
		time.Sleep(10 * time.Millisecond)
		return &fakeWarmPool{}, nil, nil
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
				assert.Failf(t, "unexpected error", "%v", err)
			}
		})
	}
	wg.Wait()

	assert.Equal(t, int32(1), okCount.Load(), "exactly one create must succeed")
	assert.Equal(t, int32(1), built.Load(),
		"pool factory must run once (ID reserved before build/start)")
	a.poolsMu.RLock()
	n := len(a.pools)
	a.poolsMu.RUnlock()
	assert.Equal(t, 1, n, "no hidden running pool")
}

func TestDeleteWarmPool_ReleasesIdleAfterShutdown(t *testing.T) {
	fake := &fakeWarmPool{}
	a, _ := newPoolTestAdapter(fake)
	createTestPool(t, a, "wp-drain")

	require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-drain", nil))

	fake.mu.Lock()
	calls := append([]string{}, fake.calls...)
	releases := fake.releases
	fake.mu.Unlock()
	assert.Equal(t, 1, releases, "idle sandboxes must not outlive the pool")
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
	require.NotEqual(t, -1, shutdownIdx, "shutdown not called: %v", calls)
	require.NotEqual(t, -1, releaseIdx, "release not called: %v", calls)
	assert.Less(t, shutdownIdx, releaseIdx, "call order = %v, want shutdown before release", calls)
}

func TestDeleteWarmPool_ReleaseError(t *testing.T) {
	fake := &fakeWarmPool{releaseErr: errors.New("release boom")}
	a, _ := newPoolTestAdapter(fake)
	createTestPool(t, a, "wp-relerr")

	err := a.DeleteWarmPool(context.Background(), "wp-relerr", nil)
	assert.Error(t, err, "release error must be surfaced")

	// The pool is still unregistered: it is shut down and must not be routed to.
	a.poolsMu.RLock()
	_, registered := a.pools["wp-relerr"]
	a.poolsMu.RUnlock()
	assert.False(t, registered)
}

func TestDeleteWarmPool_ReservesIDDuringTeardown(t *testing.T) {
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	fake := &fakeWarmPool{shutdownGate: gate, enteredShutdown: entered}
	a, _ := newPoolTestAdapter(fake)
	createTestPool(t, a, "wp-res")

	deleteDone := make(chan error, 1)
	go func() { deleteDone <- a.DeleteWarmPool(context.Background(), "wp-res", nil) }()

	<-entered // Delete is now inside Shutdown; the ID must still be reserved.
	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-res",
	})
	assert.ErrorIs(t, err, adapter.ErrWarmPoolAlreadyExists,
		"create during teardown must be rejected (shared state store is keyed by pool name)")

	close(gate)
	require.NoError(t, <-deleteDone)

	// After teardown completes the ID is free again.
	_, err = a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-res",
	})
	assert.NoError(t, err, "ID must be reusable once teardown finished")
}

func TestUpdateWarmPoolSize_ConcurrentConsistency(t *testing.T) {
	fake := &fakeWarmPool{}
	a, _ := newPoolTestAdapter(fake)
	createTestPool(t, a, "wp-consistent")

	var wg sync.WaitGroup
	for _, size := range []int{7, 3, 5} {
		wg.Go(func() {
			_ = a.UpdateWarmPoolSize(context.Background(), "wp-consistent", size)
		})
	}
	wg.Wait()

	a.poolsMu.RLock()
	entry := a.pools["wp-consistent"]
	a.poolsMu.RUnlock()
	require.NotNil(t, entry)
	entry.opMu.Lock()
	got := entry.targetSize
	entry.opMu.Unlock()
	fake.mu.Lock()
	last := fake.lastResize
	fake.mu.Unlock()
	// The recorded size must match the LAST completed SDK resize — no
	// interleaving where the entry reports a size the SDK never ended on.
	assert.Equal(t, last, got, "entry.targetSize must match last SDK Resize")
}

func TestCreateSandbox_PoolHit(t *testing.T) {
	fake := &fakeWarmPool{acquireID: "sbx-warm-1"}
	a, _ := newPoolTestAdapter(fake)
	fakeLifecycle := &fakeLifecycleClient{}
	a.lifecycle = fakeLifecycle
	createTestPool(t, a, "wp-hit")

	sb, err := a.CreateSandbox(context.Background(), &adapter.CreateSandboxRequest{TemplateID: "base"})
	require.NoError(t, err)
	assert.Equal(t, "sbx-warm-1", sb.SandboxID)
	assert.Equal(t, "opensandbox", sb.Backend)
	assert.Equal(t, "base", sb.TemplateID)
	assert.Empty(t, fakeLifecycle.createRequests, "direct create must not run on pool hit")
}

func TestCreateSandbox_PoolAcquireFails_FallsBack(t *testing.T) {
	fake := &fakeWarmPool{acquireErr: errors.New("pool exhausted")}
	a, _ := newPoolTestAdapter(fake)
	fakeLifecycle := &fakeLifecycleClient{}
	a.lifecycle = fakeLifecycle
	createTestPool(t, a, "wp-miss")

	sb, err := a.CreateSandbox(context.Background(), &adapter.CreateSandboxRequest{TemplateID: "base"})
	require.NoError(t, err)
	assert.Len(t, fakeLifecycle.createRequests, 1, "fallback to direct create")
	assert.NotEmpty(t, sb.SandboxID)
	assert.NotEqual(t, "sbx-warm-1", sb.SandboxID)
}

func TestCreateSandbox_NoPool_Unchanged(t *testing.T) {
	a, _ := newPoolTestAdapter(&fakeWarmPool{})
	fakeLifecycle := &fakeLifecycleClient{}
	a.lifecycle = fakeLifecycle

	sb, err := a.CreateSandbox(context.Background(), &adapter.CreateSandboxRequest{TemplateID: "base"})
	require.NoError(t, err)
	require.Len(t, fakeLifecycle.createRequests, 1)
	// The pool creation spec must use the resolved image, never request data.
	assert.Equal(t, "registry/base:v1", fakeLifecycle.createRequests[0].Image.URI)
	assert.NotEmpty(t, sb.SandboxID)
}

func TestCreateSandbox_OtherTemplate_Unaffected(t *testing.T) {
	fake := &fakeWarmPool{acquireID: "sbx-warm-1"}
	a, _ := newPoolTestAdapter(fake)
	fakeLifecycle := &fakeLifecycleClient{}
	a.lifecycle = fakeLifecycle
	a.templateToImage["other"] = "registry/other:v1"
	createTestPool(t, a, "wp-only-base")

	// A request for a different template must not hit the "base" pool.
	sb, err := a.CreateSandbox(context.Background(), &adapter.CreateSandboxRequest{TemplateID: "other"})
	require.NoError(t, err)
	assert.Len(t, fakeLifecycle.createRequests, 1)
	assert.NotEqual(t, "sbx-warm-1", sb.SandboxID)
}

func TestCreateSandbox_WithEnvs_BypassesPool(t *testing.T) {
	fake := &fakeWarmPool{acquireID: "sbx-warm-1"}
	a, _ := newPoolTestAdapter(fake)
	fakeLifecycle := &fakeLifecycleClient{}
	a.lifecycle = fakeLifecycle
	createTestPool(t, a, "wp-env")

	// Pool sandboxes are pre-created from the pool's fixed CreationSpec and
	// cannot carry per-request envs — such requests must go direct.
	sb, err := a.CreateSandbox(context.Background(), &adapter.CreateSandboxRequest{
		TemplateID: "base",
		Envs:       map[string]string{"FOO": "bar"},
	})
	require.NoError(t, err)
	require.Len(t, fakeLifecycle.createRequests, 1, "envs must bypass the pool")
	assert.NotEqual(t, "sbx-warm-1", sb.SandboxID)
	assert.Equal(t, "bar", fakeLifecycle.createRequests[0].Env["FOO"])
}

func TestCreateSandbox_WithMetadata_BypassesPool(t *testing.T) {
	fake := &fakeWarmPool{acquireID: "sbx-warm-1"}
	a, _ := newPoolTestAdapter(fake)
	fakeLifecycle := &fakeLifecycleClient{}
	a.lifecycle = fakeLifecycle
	createTestPool(t, a, "wp-meta")

	sb, err := a.CreateSandbox(context.Background(), &adapter.CreateSandboxRequest{
		TemplateID: "base",
		Metadata:   map[string]string{"owner": "alice"},
	})
	require.NoError(t, err)
	require.Len(t, fakeLifecycle.createRequests, 1, "metadata must bypass the pool")
	assert.NotEqual(t, "sbx-warm-1", sb.SandboxID)
}

func TestCreateSandbox_PoolImageResolutionConsistent(t *testing.T) {
	// A pool created for "MyTemplate" via the lowercased templateToImage key
	// must not be bypassed into a broken direct create when the request
	// carries envs: resolveImageURI needs the same lowercase fallback.
	a, _ := newPoolTestAdapter(&fakeWarmPool{})
	a.templateToImage = map[string]string{"mytemplate": "registry/my:v1"}
	fakeLifecycle := &fakeLifecycleClient{}
	a.lifecycle = fakeLifecycle

	_, err := a.CreateSandbox(context.Background(), &adapter.CreateSandboxRequest{
		TemplateID: "MyTemplate",
		Envs:       map[string]string{"FOO": "bar"},
	})
	require.NoError(t, err)
	require.Len(t, fakeLifecycle.createRequests, 1)
	assert.Equal(t, "registry/my:v1", fakeLifecycle.createRequests[0].Image.URI,
		"direct create must resolve the same image the pool would use")
}

// --- Architect review follow-ups (PR #82, round 2) ---

func TestLookupPoolByTemplate_DeterministicOldest(t *testing.T) {
	a, _ := newPoolTestAdapter(&fakeWarmPool{})
	createTestPool(t, a, "wp-newer")
	createTestPool(t, a, "wp-older")

	// Force a known age ordering regardless of real creation timing.
	base := time.Now()
	a.pools["wp-newer"].createdAt = base
	a.pools["wp-older"].createdAt = base.Add(-time.Hour)

	for range 50 {
		entry := a.lookupPoolByTemplate("base")
		require.NotNil(t, entry)
		assert.Same(t, a.pools["wp-older"], entry,
			"lookup must deterministically return the oldest pool")
	}
}

func TestCreateWarmPool_ResponseBuiltBeforePublication(t *testing.T) {
	// The entry must not be observable (and its targetSize mutable) before
	// the create response is assembled. Drive create and resize concurrently
	// under -race: any unsynchronized targetSize read trips the detector.
	a, _ := newPoolTestAdapter(&fakeWarmPool{})
	a.newPoolFn = func(context.Context, string, string, int) (warmPool, poolDestroyer, error) {
		time.Sleep(5 * time.Millisecond)
		return &fakeWarmPool{}, nil, nil
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			id := fmt.Sprintf("wp-pub%d", i)
			_, _ = a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
				TemplateID: "base", TargetSize: 2, WarmPoolID: id,
			})
			_ = a.UpdateWarmPoolSize(context.Background(), id, 4)
			_, _ = a.GetWarmPool(context.Background(), id)
		})
	}
	wg.Wait()
}

// --- Round-3 review remediation tests (PR #82) ---

// failingTemplateStore simulates a template store outage. Embedded interface
// methods that are not overridden panic if called; the pool-hit path must
// never reach them.
type failingTemplateStore struct{ TemplateStore }

func (failingTemplateStore) GetTemplate(context.Context, string) (*TemplateEntry, error) {
	return nil, errors.New("store down")
}

func (failingTemplateStore) ResolveAlias(context.Context, string) (string, error) {
	return "", errors.New("store down")
}

func TestCreateSandbox_PoolUsableWhenTemplateStoreFails(t *testing.T) {
	ctx := context.Background()
	fake := &fakeWarmPool{acquireID: "sbx-warm-1"}
	a, _ := newPoolTestAdapter(fake)
	a.templateToImage = map[string]string{} // pool image comes from the store
	store := NewMemoryTemplateStore()
	require.NoError(t, store.CreateTemplate(ctx, &TemplateEntry{
		TemplateID: "base", ImageURI: "registry/base:v1",
	}))
	a.templates = store
	createTestPool(t, a, "wp-store")

	// The store goes down after the pool was created. Image resolution now
	// happens only on the direct-create path, so the ready pool must still
	// serve requests.
	a.templates = failingTemplateStore{}
	fakeLifecycle := &fakeLifecycleClient{}
	a.lifecycle = fakeLifecycle

	sb, err := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "base"})
	require.NoError(t, err, "a failing template store must not shadow a ready pool")
	assert.Equal(t, "sbx-warm-1", sb.SandboxID)
	assert.Empty(t, fakeLifecycle.createRequests)
}

func TestAcquireSandboxTimeout_MatchesDirectCreate(t *testing.T) {
	// The direct-create path passes opensandbox.DefaultTimeoutSeconds as the
	// sandbox Timeout; acquisition must renew to the same lifetime so both
	// paths hand out sandboxes with consistent TTLs.
	assert.Equal(t, opensandbox.DefaultTimeoutSeconds*time.Second, acquireSandboxTimeout)
	assert.Positive(t, acquireSandboxTimeout,
		"SDK renews acquired sandboxes only when SandboxTimeout > 0")
}

func TestPoolKeyPrefix_ImageNamespaced(t *testing.T) {
	a := poolKeyPrefix("", "registry/base:v1")
	b := poolKeyPrefix("", "registry/base:v1")
	c := poolKeyPrefix("", "registry/base:v2")
	d := poolKeyPrefix("custom", "registry/base:v1")

	assert.Equal(t, a, b, "same image must yield a stable prefix (restart reuse)")
	assert.NotEqual(t, a, c, "different images must yield different namespaces")
	assert.True(t, strings.HasPrefix(a, "opensandbox:pool:img-"), "default prefix kept: %q", a)
	assert.True(t, strings.HasPrefix(d, "custom:img-"), "configured prefix kept: %q", d)
}

func TestDeleteWarmPool_GracefulAndForce(t *testing.T) {
	t.Run("default is graceful shutdown plus drain", func(t *testing.T) {
		fake := &fakeWarmPool{}
		a, _ := newPoolTestAdapter(fake)
		createTestPool(t, a, "wp-g")

		require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-g", nil))
		assert.Equal(t, []bool{true}, fake.shutdownGracefulFlags())
		assert.Equal(t, 1, fake.releases, "idle queue must be drained")
	})
	t.Run("force skips the in-flight drain", func(t *testing.T) {
		fake := &fakeWarmPool{}
		a, dest := newPoolTestAdapter(fake)
		createTestPool(t, a, "wp-f")

		require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-f",
			&adapter.WarmPoolDeleteOptions{Force: true}))
		assert.Equal(t, []bool{false}, fake.shutdownGracefulFlags(),
			"force must call Shutdown(graceful=false)")
		// PoolManager.Destroy performs the store-level idle drain and kill, so
		// the in-process ReleaseAllIdleParallel loop is no longer needed.
		assert.Zero(t, fake.releases, "Destroy replaces the in-process drain")
		names, _ := dest.snapshot()
		assert.Equal(t, []string{"wp-f"}, names, "force must destroy the pool namespace")
	})
}

func TestDeleteWarmPool_ForceUsesPoolManagerDestroy(t *testing.T) {
	fake := &fakeWarmPool{}
	a, dest := newPoolTestAdapter(fake)
	createTestPool(t, a, "wp-fd")

	require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-fd",
		&adapter.WarmPoolDeleteOptions{Force: true}))

	assert.Equal(t, []bool{false}, fake.shutdownGracefulFlags(), "force shuts down non-gracefully")
	names, opts := dest.snapshot()
	require.Equal(t, []string{"wp-fd"}, names, "force delete must Destroy the pool namespace")
	assert.Equal(t, opensandbox.PoolDestroyForce, opts[0].Strategy)
	require.NotNil(t, opts[0].TombstoneTTL)
	assert.Equal(t, forceDeleteTombstoneTTL, *opts[0].TombstoneTTL,
		"tombstone TTL must be the short gateway value, not the 7-day SDK default")
	require.NotNil(t, opts[0].DrainTimeout,
		"drain budget must be explicit: the 30s SDK default is too tight for a max-size pool drained sequentially")
	assert.Equal(t, forceDeleteDrainTimeout, *opts[0].DrainTimeout)
	assert.Zero(t, fake.releases, "Destroy replaces ReleaseAllIdleParallel on the force path")
}

func TestDeleteWarmPool_GracefulDoesNotDestroy(t *testing.T) {
	fake := &fakeWarmPool{}
	a, dest := newPoolTestAdapter(fake)
	createTestPool(t, a, "wp-gd")

	require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-gd", nil))

	assert.Equal(t, []bool{true}, fake.shutdownGracefulFlags())
	assert.Equal(t, 1, fake.releases, "graceful keeps the in-process drain")
	names, _ := dest.snapshot()
	assert.Empty(t, names, "graceful must not write a tombstone")
}

func TestDeleteWarmPool_DestroyErrorPropagates(t *testing.T) {
	fake := &fakeWarmPool{}
	a, dest := newPoolTestAdapter(fake)
	dest.err = errors.New("store down")
	createTestPool(t, a, "wp-err")

	err := a.DeleteWarmPool(context.Background(), "wp-err",
		&adapter.WarmPoolDeleteOptions{Force: true})
	assert.ErrorContains(t, err, "destroying warm pool")
}

func TestCreateWarmPool_RejectedByTombstone(t *testing.T) {
	fake := &fakeWarmPool{startErr: &opensandbox.PoolDestroyedError{
		PoolName: "wp-t", State: opensandbox.PoolDestroyStateDestroyed,
	}}
	a, _ := newPoolTestAdapter(fake)

	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-t",
	})
	assert.ErrorContains(t, err, "force-deleted recently")
}

func TestCreateWarmPool_RecoversStalledDestroy(t *testing.T) {
	// A gateway restart drops the process-local poolDestroyPending map; with a
	// shared (Redis) store the namespace can be stuck DESTROYING — a fence
	// with no expiry. CreateWarmPool must resume the idempotent Destroy so
	// the namespace converges to a bounded tombstone instead of staying
	// permanently unusable.
	fake := &fakeWarmPool{startErr: &opensandbox.PoolDestroyedError{
		PoolName: "wp-stall", State: opensandbox.PoolDestroyStateDestroying,
	}}
	a, _ := newPoolTestAdapter(fake)
	recovery := &fakeDestroyer{}
	a.newDestroyerFn = func(string) (poolDestroyer, error) { return recovery, nil }

	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-stall",
	})
	assert.ErrorContains(t, err, "teardown was interrupted")

	names, opts := recovery.snapshot()
	assert.Equal(t, []string{"wp-stall"}, names, "stalled Destroy must be resumed")
	require.Len(t, opts, 1)
	assert.Equal(t, opensandbox.PoolDestroyForce, opts[0].Strategy)
	require.NotNil(t, opts[0].TombstoneTTL)
	assert.Equal(t, forceDeleteTombstoneTTL, *opts[0].TombstoneTTL)
}

func TestNew_ValidatesPoolStateStoreConfig(t *testing.T) {
	t.Run("password-bearing remote redis without tls fails at construction", func(t *testing.T) {
		_, err := New(AdapterConfig{
			Name:    "opensandbox",
			BaseURL: "http://localhost:8080",
			PoolStateStore: &PoolStateStoreConfig{
				Type: "redis", Addr: "redis.example.com:6379", Password: "secret",
			},
		})
		assert.ErrorContains(t, err, "cleartext")
	})
	t.Run("tls enabled passes construction", func(t *testing.T) {
		a, err := New(AdapterConfig{
			Name:    "opensandbox",
			BaseURL: "http://localhost:8080",
			PoolStateStore: &PoolStateStoreConfig{
				Type: "redis", Addr: "redis.example.com:6379", Password: "secret", EnableTLS: true,
			},
		})
		require.NoError(t, err)
		assert.NotNil(t, a)
	})
	t.Run("unknown type fails at construction", func(t *testing.T) {
		_, err := New(AdapterConfig{
			Name:           "opensandbox",
			BaseURL:        "http://localhost:8080",
			PoolStateStore: &PoolStateStoreConfig{Type: "etcd"},
		})
		assert.ErrorContains(t, err, "unknown pool stateStore type")
	})
}

func TestDeleteWarmPool_ForceDestroyFailureRetryableViaDelete(t *testing.T) {
	fake := &fakeWarmPool{}
	a, dest := newPoolTestAdapter(fake)
	dest.err = errors.New("store down")
	createTestPool(t, a, "wp-retry")

	// First force delete: Shutdown succeeds, Destroy fails — the entry is
	// unregistered but the namespace is stuck DESTROYING.
	err := a.DeleteWarmPool(context.Background(), "wp-retry",
		&adapter.WarmPoolDeleteOptions{Force: true})
	require.ErrorContains(t, err, "destroying warm pool")
	_, getErr := a.GetWarmPool(context.Background(), "wp-retry")
	assert.ErrorIs(t, getErr, adapter.ErrWarmPoolNotFound)

	// A repeated DELETE must resume the idempotent Destroy instead of
	// reporting NotFound and leaving the fence behind forever.
	dest.err = nil
	require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-retry",
		&adapter.WarmPoolDeleteOptions{Force: true}))
	names, _ := dest.snapshot()
	assert.Equal(t, []string{"wp-retry", "wp-retry"}, names)

	// Once the retry completed, the pending entry is consumed.
	err = a.DeleteWarmPool(context.Background(), "wp-retry",
		&adapter.WarmPoolDeleteOptions{Force: true})
	assert.ErrorIs(t, err, adapter.ErrWarmPoolNotFound)
}

func TestCreateWarmPool_RejectedWhileDestroyPending(t *testing.T) {
	fake := &fakeWarmPool{}
	a, dest := newPoolTestAdapter(fake)
	dest.err = errors.New("store down")
	createTestPool(t, a, "wp-pend")
	require.Error(t, a.DeleteWarmPool(context.Background(), "wp-pend",
		&adapter.WarmPoolDeleteOptions{Force: true}))

	// While destruction is pending (namespace possibly unfenced), creating a
	// replacement under the same ID must be refused: the pending retry would
	// otherwise destroy the replacement's namespace out from under it.
	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-pend",
	})
	assert.ErrorContains(t, err, "being destroyed")

	// Completing the pending destroy unblocks creation again.
	dest.err = nil
	require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-pend",
		&adapter.WarmPoolDeleteOptions{Force: true}))
	_, err = a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-pend",
	})
	require.NoError(t, err)
}

func TestDeleteWarmPool_PendingRetryReservesID(t *testing.T) {
	fake := &fakeWarmPool{}
	a, dest := newPoolTestAdapter(fake)
	dest.err = errors.New("store down")
	createTestPool(t, a, "wp-res")
	require.Error(t, a.DeleteWarmPool(context.Background(), "wp-res",
		&adapter.WarmPoolDeleteOptions{Force: true}))

	// Second DELETE: Destroy blocks mid-retry; while it runs, the ID must be
	// reserved so a concurrent CreateWarmPool cannot register a replacement
	// that the retry would then destroy.
	dest.err = nil
	gate := make(chan struct{})
	dest.block = gate
	retryDone := make(chan error, 1)
	go func() {
		retryDone <- a.DeleteWarmPool(context.Background(), "wp-res",
			&adapter.WarmPoolDeleteOptions{Force: true})
	}()
	// Wait until the retry is inside Destroy.
	require.Eventually(t, func() bool {
		names, _ := dest.snapshot()
		return len(names) == 2
	}, 2*time.Second, 5*time.Millisecond)

	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-res",
	})
	assert.ErrorIs(t, err, adapter.ErrWarmPoolAlreadyExists,
		"ID must stay reserved while the pending destroy retry runs")

	// A third concurrent DELETE must be rejected as already-in-progress
	// (retries for one ID are serialized), not run concurrently.
	err = a.DeleteWarmPool(context.Background(), "wp-res",
		&adapter.WarmPoolDeleteOptions{Force: true})
	assert.ErrorContains(t, err, "already in progress")

	close(gate)
	require.NoError(t, <-retryDone)

	// Reservation released with the pending entry consumed.
	err = a.DeleteWarmPool(context.Background(), "wp-res",
		&adapter.WarmPoolDeleteOptions{Force: true})
	assert.ErrorIs(t, err, adapter.ErrWarmPoolNotFound)
}

func TestDeleteWarmPool_ShutdownAndDestroyBothFail_PendingRegistered(t *testing.T) {
	fake := &fakeWarmPool{shutdownErr: errors.New("shutdown boom")}
	a, dest := newPoolTestAdapter(fake)
	dest.err = errors.New("store down")
	createTestPool(t, a, "wp-both")

	// Force delete with BOTH Shutdown and Destroy failing: the shutdown error
	// surfaces, but the failed Destroy must still register the pending retry —
	// otherwise the namespace leaks with no path to resume it.
	err := a.DeleteWarmPool(context.Background(), "wp-both",
		&adapter.WarmPoolDeleteOptions{Force: true})
	require.ErrorContains(t, err, "shutting down warm pool")

	dest.err = nil
	fake.shutdownErr = nil
	require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-both",
		&adapter.WarmPoolDeleteOptions{Force: true}),
		"repeated DELETE must resume the pending Destroy")
	names, _ := dest.snapshot()
	assert.Equal(t, []string{"wp-both", "wp-both"}, names)
}
