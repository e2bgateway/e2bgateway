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
	"fmt"
	"net/url"
	"time"

	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
	"github.com/alibaba/OpenSandbox/sdks/sandbox/go/poolredis"
	"github.com/redis/go-redis/v9"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
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
	if a.poolStoreCfg == nil || a.poolStoreCfg.Type == "" || a.poolStoreCfg.Type == storeTypeMemory {
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

// poolStatus maps SDK pool state to the unified status vocabulary
// (pending / scaling / ready / degraded / destroyed).
func poolStatus(snap *opensandbox.PoolSnapshot) string {
	switch snap.LifecycleState {
	case opensandbox.PoolLifecycleRunning:
		if snap.HealthState == opensandbox.PoolDegraded {
			return adapter.WarmPoolStatusDegraded
		}
		if snap.IdleCount < snap.MaxIdle {
			return adapter.WarmPoolStatusScaling
		}
		return adapter.WarmPoolStatusReady
	case opensandbox.PoolLifecycleStarting, opensandbox.PoolLifecycleNotStarted:
		return adapter.WarmPoolStatusPending
	case opensandbox.PoolLifecycleDraining, opensandbox.PoolLifecycleStopped:
		return adapter.WarmPoolStatusDestroyed
	default:
		return adapter.WarmPoolStatusPending
	}
}

// defaultNewPool builds a real SDK pool. It is the production value of
// Adapter.newPoolFn; tests substitute fakes.
func (a *Adapter) defaultNewPool(_ context.Context, poolID, imageURI string, size int) (warmPool, error) {
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

// CreateWarmPool builds and starts an in-process SDK pool serving templateID.
// The pool pre-warms up to TargetSize sandboxes; CreateSandbox acquires from
// it when a pool matches the requested template.
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

	// CreationSpec only comes from server-side config (templateToImage /
	// template store), never from the request body.
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
