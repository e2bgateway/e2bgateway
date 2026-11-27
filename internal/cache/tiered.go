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

package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/e2bgateway/e2bgateway/internal/metrics"
)

// Hot-path TTLs from design doc §15.2.
const (
	SandboxMetadataTTL = 30 * time.Second
	TemplateListTTL    = 60 * time.Second
	BackendHealthTTL   = 10 * time.Second
)

// Broadcaster publishes cross-replica invalidations (implemented by *Invalidator).
type Broadcaster interface {
	Broadcast(ctx context.Context, cacheName, key string)
}

// TieredOptions configures a TieredCache. L2 nil => L1-only mode.
type TieredOptions struct {
	Name        string // metrics label: sandbox_metadata / template_list
	L1MaxSize   int
	L1TTL       time.Duration
	L2          *RedisCache
	Timeout     time.Duration // per-L2-operation timeout, default 200ms
	Invalidator Broadcaster   // nil => no cross-replica broadcast
}

// TieredCache composes L1 (in-memory LRU) and L2 (shared Redis) with
// write-through writes and Pub/Sub cross-replica invalidation. Any L2
// failure degrades to L1 + backend load; it never fails a request.
type TieredCache[T any] struct {
	name    string
	l1      *Cache
	l2      *RedisCache
	timeout time.Duration
	inv     Broadcaster
	sf      singleflight.Group

	mu       sync.Mutex
	lastWarn time.Time
}

// NewTiered creates a tiered cache. T must be JSON-serializable for L2.
func NewTiered[T any](opts TieredOptions) *TieredCache[T] {
	if opts.Timeout <= 0 {
		opts.Timeout = 200 * time.Millisecond
	}
	return &TieredCache[T]{
		name:    opts.Name,
		l1:      New(opts.L1MaxSize, opts.L1TTL),
		l2:      opts.L2,
		timeout: opts.Timeout,
		inv:     opts.Invalidator,
	}
}

// GetOrLoad reads L1 → L2 (backfilling L1) → load (backfilling L2 then L1).
// Concurrent loads of the same key are merged via singleflight.
func (tc *TieredCache[T]) GetOrLoad(ctx context.Context, key string, ttl time.Duration, load func(context.Context) (T, error)) (T, error) {
	var zero T
	if v, ok := tc.l1.Get(key); ok {
		metrics.RecordCache(tc.name, "l1_hit")
		return v.(T), nil //nolint:errcheck // we control what is stored
	}
	if tc.l2 != nil {
		if raw, hit := tc.l2Get(ctx, key); hit {
			var v T
			if err := json.Unmarshal([]byte(raw), &v); err != nil {
				tc.warn("decode", fmt.Errorf("key %q: %w", key, err))
			} else {
				tc.l1.SetWithTTL(key, v, ttl)
				metrics.RecordCache(tc.name, "l2_hit")
				return v, nil
			}
		}
	}
	metrics.RecordCache(tc.name, "miss")
	v, err, _ := tc.sf.Do(key, func() (any, error) {
		val, loadErr := load(ctx)
		if loadErr != nil {
			return nil, loadErr
		}
		tc.fill(ctx, key, val, ttl)
		return val, nil
	})
	if err != nil {
		return zero, err
	}
	return v.(T), nil //nolint:errcheck // singleflight returns what load produced
}

// Set writes through: L2 first, then L1, then broadcasts an invalidation so
// other replicas drop their (now stale) L1 copy and re-read from L2.
func (tc *TieredCache[T]) Set(ctx context.Context, key string, val T, ttl time.Duration) {
	tc.fill(ctx, key, val, ttl)
	tc.broadcast(ctx, key)
}

// Invalidate removes the key from both tiers and broadcasts the removal.
func (tc *TieredCache[T]) Invalidate(ctx context.Context, key string) {
	if tc.l2 != nil {
		cctx, cancel := context.WithTimeout(ctx, tc.timeout)
		err := tc.l2.Delete(cctx, key)
		cancel()
		if err != nil {
			metrics.RecordCache(tc.name, "l2_error")
			tc.warn("delete", err)
		}
	}
	tc.l1.Delete(key)
	tc.broadcast(ctx, key)
}

// OnRemoteInvalidate applies an invalidation broadcast from another replica.
// Registered with Invalidator.Register under the cache's Name.
func (tc *TieredCache[T]) OnRemoteInvalidate(key string) {
	tc.l1.Delete(key)
	metrics.RecordCache(tc.name, "invalidation")
}

func (tc *TieredCache[T]) l2Get(ctx context.Context, key string) (string, bool) {
	cctx, cancel := context.WithTimeout(ctx, tc.timeout)
	defer cancel()
	raw, hit, err := tc.l2.Get(cctx, key)
	if err != nil {
		metrics.RecordCache(tc.name, "l2_error")
		tc.warn("get", err)
		return "", false
	}
	return raw, hit
}

// fill backfills L2 first, then L1 (spec §3.2). L2 failures are logged and
// metered but never propagated.
func (tc *TieredCache[T]) fill(ctx context.Context, key string, val T, ttl time.Duration) {
	if tc.l2 != nil {
		raw, err := json.Marshal(val)
		if err != nil {
			metrics.RecordCache(tc.name, "l2_error")
			tc.warn("encode", err)
		} else {
			cctx, cancel := context.WithTimeout(ctx, tc.timeout)
			err = tc.l2.SetWithTTL(cctx, key, string(raw), ttl)
			cancel()
			if err != nil {
				metrics.RecordCache(tc.name, "l2_error")
				tc.warn("set", err)
			}
		}
	}
	tc.l1.SetWithTTL(key, val, ttl)
}

func (tc *TieredCache[T]) broadcast(ctx context.Context, key string) {
	if tc.inv == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, tc.timeout)
	defer cancel()
	tc.inv.Broadcast(cctx, tc.name, key)
}

// warn logs L2 degradation at most once per 10s per cache instance.
func (tc *TieredCache[T]) warn(op string, err error) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if time.Since(tc.lastWarn) < 10*time.Second {
		return
	}
	tc.lastWarn = time.Now()
	slog.Warn("tiered cache L2 degraded", "cache", tc.name, "op", op, "error", err)
}
