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
