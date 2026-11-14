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
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
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
	ReleaseAllIdleParallel(ctx context.Context, maxWorkers int) (int, error)
}

// poolDestroyer is the adapter-facing subset of
// *opensandbox.SandboxPoolManager. Destroy fences the pool namespace across
// every peer sharing the state store, drains and kills idle sandboxes, and
// writes a DESTROYED tombstone. Tests substitute fakes.
type poolDestroyer interface {
	Destroy(ctx context.Context, poolName string,
		options opensandbox.PoolDestroyOptions) (*opensandbox.PoolDestroyResult, error)
}

// forceDeleteTombstoneTTL bounds how long a force-deleted pool namespace
// refuses rebinding. The SDK default (7 days) would block intentional
// same-ID recreation far too long; 5 minutes covers the concurrent-teardown
// window while letting an administrator recreate the pool soon after.
const forceDeleteTombstoneTTL = 5 * time.Minute

// forceDeleteDrainTimeout bounds the Destroy drain phase. The SDK kills idle
// sandboxes sequentially under this budget; its 30s default is too tight for
// a max-size pool (DefaultMaxWarmPoolSize=256 × ~150ms per kill ≈ 38s), which
// would fail otherwise-normal force deletes. 2 minutes keeps >3x headroom at
// max size while the request context still bounds pathological hangs.
const forceDeleteDrainTimeout = 2 * time.Minute

// forceDestroyOptions builds the Destroy options shared by the inline force
// delete and the pending-destroy retry path.
func forceDestroyOptions() opensandbox.PoolDestroyOptions {
	ttl := forceDeleteTombstoneTTL
	drain := forceDeleteDrainTimeout
	return opensandbox.PoolDestroyOptions{
		Strategy:     opensandbox.PoolDestroyForce,
		DrainTimeout: &drain,
		TombstoneTTL: &ttl,
	}
}

// poolReleaseWorkers bounds concurrency when draining idle sandboxes on
// delete/rollback.
const poolReleaseWorkers = 8

// acquireSandboxTimeout mirrors the lifetime the direct-create path applies
// (opensandbox.DefaultTimeoutSeconds). The SDK only renews an acquired
// sandbox when AcquireOptions.SandboxTimeout > 0; warmup sets the TTL once
// from the pool IdleTimeout, so without this a long-idle sandbox could be
// handed out with almost no lifetime left.
const acquireSandboxTimeout = opensandbox.DefaultTimeoutSeconds * time.Second

// sdkWarmPool adapts an opensandbox.DefaultSandboxPool to warmPool. The
// concrete type is required because ReleaseAllIdleParallel (blocking drain)
// is not part of the opensandbox.SandboxPool interface.
type sdkWarmPool struct {
	inner *opensandbox.DefaultSandboxPool
}

var _ warmPool = (*sdkWarmPool)(nil)

func (p *sdkWarmPool) Start(ctx context.Context) error { return p.inner.Start(ctx) }

func (p *sdkWarmPool) AcquireID(ctx context.Context) (string, error) {
	sb, err := p.inner.Acquire(ctx, opensandbox.AcquireOptions{
		SandboxTimeout: acquireSandboxTimeout,
	})
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

func (p *sdkWarmPool) ReleaseAllIdleParallel(ctx context.Context, maxWorkers int) (int, error) {
	return p.inner.ReleaseAllIdleParallel(ctx, maxWorkers)
}

// poolEntry is a registered warm pool.
type poolEntry struct {
	pool       warmPool
	templateID string
	createdAt  time.Time

	// destroyer retires the pool's state-store namespace on force delete
	// (fence + drain + tombstone). Shares the pool's state store instance.
	destroyer poolDestroyer

	// opMu serializes mutating operations on this entry (Resize + targetSize
	// publication, Delete's shutdown/drain) and guards targetSize reads.
	// Lock order is always poolsMu → opMu, never the reverse.
	opMu       sync.Mutex
	targetSize int
}

// PoolStateStoreConfig configures the opensandbox pool state store.
type PoolStateStoreConfig struct {
	Type      string // "memory" (default) | "redis"
	Addr      string
	Password  string
	DB        int
	KeyPrefix string
	// EnableTLS turns on TLS for the Redis connection. Required (or use a
	// rediss:// Addr) when Password is set and Addr is not loopback.
	EnableTLS bool
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

// poolKeyPrefix returns the effective Redis key prefix for a pool's persisted
// state: the configured prefix namespaced by an image fingerprint. The SDK
// keys idle-sandbox records by pool name only; without the image component a
// gateway restart that recreated the same pool ID with a changed
// template→image mapping could hand out sandboxes built from the OLD image
// while reporting the new pool's template (PR #82 review). Stale-namespace
// entries become unreachable and expire server-side at IdleTimeout.
func poolKeyPrefix(configured, imageURI string) string {
	if configured == "" {
		configured = "opensandbox:pool"
	}
	sum := sha256.Sum256([]byte(imageURI))
	return fmt.Sprintf("%s:img-%x", configured, sum[:8])
}

// validatePoolStateStoreConfig checks the pool stateStore configuration
// without building a client, so New() can reject bad configs (unknown type,
// missing addr, password-bearing remote Redis without TLS) at adapter
// construction instead of on the first CreateWarmPool.
func validatePoolStateStoreConfig(cfg *PoolStateStoreConfig) error {
	if cfg == nil || cfg.Type == "" || cfg.Type == storeTypeMemory {
		return nil
	}
	if cfg.Type != "redis" {
		return fmt.Errorf("unknown pool stateStore type %q (supported: memory, redis)", cfg.Type)
	}
	if cfg.Addr == "" {
		return fmt.Errorf("pool stateStore redis requires addr")
	}
	addr, schemeTLS := normalizeRedisAddr(cfg.Addr)
	if _, err := redisTLSConfig(addr, cfg.EnableTLS || schemeTLS, cfg.Password != ""); err != nil {
		return err
	}
	return nil
}

// buildPoolStateStore returns the pool state store to bind a pool (and its
// PoolManager) to. The store's key namespace is bound to imageURI (see
// poolKeyPrefix). It never returns a nil store on success.
func (a *Adapter) buildPoolStateStore(imageURI string) (opensandbox.PoolStateStore, error) {
	if a.poolStoreCfg == nil || a.poolStoreCfg.Type == "" || a.poolStoreCfg.Type == storeTypeMemory {
		// Explicit (not nil) so the pool and its PoolManager share the same
		// store instance — the manager's fence/tombstone must be visible to
		// the pool's reconcile loop.
		return opensandbox.NewInMemoryPoolStateStore(), nil
	}
	if err := validatePoolStateStoreConfig(a.poolStoreCfg); err != nil {
		return nil, err
	}
	addr, schemeTLS := normalizeRedisAddr(a.poolStoreCfg.Addr)
	tlsCfg, err := redisTLSConfig(addr, a.poolStoreCfg.EnableTLS || schemeTLS, a.poolStoreCfg.Password != "")
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(&redis.Options{
		Addr:      addr,
		Password:  a.poolStoreCfg.Password,
		DB:        a.poolStoreCfg.DB,
		TLSConfig: tlsCfg,
	})
	keyPrefix := poolKeyPrefix(a.poolStoreCfg.KeyPrefix, imageURI)
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
// Adapter.newPoolFn; tests substitute fakes. TestDefaultNewPool_Builds
// exercises this factory against the real SDK builder so validation changes
// in the SDK (e.g. rejected CreationSpec fields) cannot slip past fake-backed
// tests again.
func (a *Adapter) defaultNewPool(_ context.Context, poolID, imageURI string, size int) (warmPool, poolDestroyer, error) {
	cc, err := a.poolConnectionConfig()
	if err != nil {
		return nil, nil, err
	}
	// NOTE: CreationSpec.TimeoutSeconds must NOT be set — SDK v1.2.0 Build()
	// rejects it. Sandbox TTL in pools is controlled by IdleTimeout; orphaned
	// sandboxes expire server-side when it elapses.
	b := opensandbox.NewSandboxPoolBuilder().
		PoolName(poolID).
		MaxIdle(size).
		ConnectionConfig(cc).
		CreationSpec(opensandbox.PoolCreationSpec{
			Image:          imageURI,
			Entrypoint:     opensandbox.DefaultEntrypoint,
			ResourceLimits: opensandbox.DefaultResourceLimits,
		})
	if a.poolIdleTimeout > 0 {
		b = b.IdleTimeout(a.poolIdleTimeout)
	}
	store, err := a.buildPoolStateStore(imageURI)
	if err != nil {
		return nil, nil, err
	}
	// The pool and the manager must share ONE store instance: the manager's
	// force-delete fence/tombstone is only observable by the pool's reconcile
	// loop when they coordinate through the same state store.
	b = b.StateStore(store)
	p, err := b.Build()
	if err != nil {
		return nil, nil, fmt.Errorf("building pool %q: %w", poolID, err)
	}
	mgr, err := opensandbox.NewSandboxPoolManagerBuilder().
		StateStore(store).
		ConnectionConfig(cc).
		Build()
	if err != nil {
		return nil, nil, fmt.Errorf("building pool manager %q: %w", poolID, err)
	}
	return &sdkWarmPool{inner: p}, mgr, nil
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

	// Reserve the ID before the slow build/start. The registry entry only
	// appears after startup, so without this reservation two concurrent
	// creates could both pass the existence check, both start a pool, and
	// the second registration would hide the first (leaking a live
	// reconciling pool that Delete can no longer reach).
	a.poolsMu.Lock()
	if _, exists := a.pools[id]; exists {
		a.poolsMu.Unlock()
		return nil, adapter.ErrWarmPoolAlreadyExists
	}
	if _, starting := a.poolStarting[id]; starting {
		a.poolsMu.Unlock()
		return nil, adapter.ErrWarmPoolAlreadyExists
	}
	a.poolStarting[id] = struct{}{}
	a.poolsMu.Unlock()
	defer func() {
		a.poolsMu.Lock()
		delete(a.poolStarting, id)
		a.poolsMu.Unlock()
	}()

	// CreationSpec only comes from server-side config (templateToImage /
	// template store), never from the request body.
	imageURI, err := a.resolvePoolImageURI(ctx, req.TemplateID)
	if err != nil {
		return nil, err
	}
	pool, destroyer, err := a.newPoolFn(ctx, id, imageURI, size)
	if err != nil {
		return nil, fmt.Errorf("building warm pool %q: %w", id, err)
	}
	if err := pool.Start(ctx); err != nil {
		// A DESTROYED/DESTROYING namespace (force delete still in flight on a
		// peer, or the tombstone has not expired yet) fails Start with
		// PoolDestroyedError. Shut the half-started pool down and surface an
		// actionable retry message instead of a raw SDK error.
		if _, ok := errors.AsType[*opensandbox.PoolDestroyedError](err); ok {
			_ = pool.Shutdown(ctx, false)
			return nil, fmt.Errorf("warm pool %q was force-deleted recently; retry after the tombstone expires (~%v) or choose a different warmPoolID", id, forceDeleteTombstoneTTL)
		}
		return nil, fmt.Errorf("starting warm pool %q: %w", id, err)
	}

	entry := &poolEntry{
		pool:       pool,
		destroyer:  destroyer,
		templateID: req.TemplateID,
		targetSize: size,
		createdAt:  time.Now(),
	}

	// Build the response from a single snapshot BEFORE registering. If the
	// snapshot fails, roll the live pool back: returning an error after
	// registration would leave a running pool the caller believes was never
	// created (and a retry would hit ErrWarmPoolAlreadyExists).
	snap, err := entry.pool.Snapshot(ctx)
	if err != nil {
		_ = pool.Shutdown(ctx, false)
		// Reconciliation may have parked idle sandboxes in the store before
		// the snapshot failed; drain them so a retry under the same ID cannot
		// acquire an orphan (possibly built from a stale image mapping).
		// Best-effort: kill attempts are asynchronous server-side, and any
		// survivor expires at IdleTimeout.
		_, _ = pool.ReleaseAllIdleParallel(ctx, poolReleaseWorkers)
		return nil, fmt.Errorf("snapshotting new warm pool %q: %w", id, err)
	}

	// Assemble the response while the entry is still private: reading
	// entry.targetSize after publication would race with a concurrent
	// UpdateWarmPoolSize (which writes it under opMu).
	wp := entryToWarmPool(id, entry, snap)

	a.poolsMu.Lock()
	a.pools[id] = entry
	a.poolsMu.Unlock()

	return wp, nil
}

// entryToWarmPool assembles the DTO from an entry and its snapshot.
// entry.targetSize must be read under entry.opMu when concurrent resizes are
// possible; callers here hold a fresh entry or the lock.
func entryToWarmPool(id string, entry *poolEntry, snap *opensandbox.PoolSnapshot) *adapter.WarmPool {
	return &adapter.WarmPool{
		WarmPoolID:  id,
		TemplateID:  entry.templateID,
		TargetSize:  entry.targetSize,
		CurrentSize: snap.IdleCount,
		Status:      poolStatus(snap),
		CreatedAt:   entry.createdAt,
	}
}

// GetWarmPool snapshots a registered pool.
func (a *Adapter) GetWarmPool(ctx context.Context, warmPoolID string) (*adapter.WarmPool, error) {
	a.poolsMu.RLock()
	entry, ok := a.pools[warmPoolID]
	a.poolsMu.RUnlock()
	if !ok {
		return nil, adapter.ErrWarmPoolNotFound
	}
	// Hold opMu across the snapshot AND the targetSize read: taking the
	// lock only after Snapshot would let a resize complete in between and
	// pair a stale idle-count/status with the new target size.
	entry.opMu.Lock()
	defer entry.opMu.Unlock()
	snap, err := entry.pool.Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshotting warm pool %q: %w", warmPoolID, err)
	}
	return entryToWarmPool(warmPoolID, entry, snap), nil
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
			// The pool was deleted between the ID snapshot and this read;
			// skip it rather than failing the whole listing. Other errors
			// (e.g. snapshot failures) still surface.
			if errors.Is(err, adapter.ErrWarmPoolNotFound) {
				continue
			}
			return nil, err
		}
		pools = append(pools, wp)
	}
	return pools, nil
}

// DeleteWarmPool shuts a pool down, drains its idle sandboxes, and
// unregisters it. A non-nil opts with Force set skips the graceful drain of
// in-flight work (Shutdown graceful=false) and retires the pool namespace
// store-side through PoolManager.Destroy (fence + drain + tombstone) instead
// of the in-process idle release.
func (a *Adapter) DeleteWarmPool(ctx context.Context, warmPoolID string, opts *adapter.WarmPoolDeleteOptions) error {
	force := opts != nil && opts.Force
	a.poolsMu.Lock()
	entry, ok := a.pools[warmPoolID]
	if ok {
		delete(a.pools, warmPoolID)
		// Keep the ID reserved until teardown finishes. The state store is
		// keyed by pool name, so a pool recreated under the same ID while
		// the old one is still draining would have its fresh idle entries
		// removed by the old pool's drain.
		a.poolStarting[warmPoolID] = struct{}{}
	}
	pendingDestroyer, pending := a.poolDestroyPending[warmPoolID]
	a.poolsMu.Unlock()
	if !ok {
		if !pending {
			return adapter.ErrWarmPoolNotFound
		}
		// A previous force delete failed mid-Destroy, leaving the namespace
		// DESTROYING (fenced, possibly with idle sandboxes still queued).
		// Destroy is idempotent and retry-safe per the SDK contract, so a
		// repeated DELETE resumes it instead of 404ing and leaking the fence.
		return a.runPendingDestroy(ctx, warmPoolID, pendingDestroyer)
	}
	defer func() {
		a.poolsMu.Lock()
		delete(a.poolStarting, warmPoolID)
		a.poolsMu.Unlock()
	}()

	// opMu keeps a concurrent Resize from interleaving with teardown.
	entry.opMu.Lock()
	defer entry.opMu.Unlock()

	// Shutdown first so the reconciler stops and cannot replenish slots we
	// are about to release. NOTE: graceful Shutdown drains in-flight work but
	// does NOT touch the idle queue — without an explicit drain the idle
	// sandboxes would stay alive until IdleTimeout, and with a shared Redis
	// state store a recreated pool ID could acquire stale sandboxes built
	// from an old image. ReleaseAllIdleParallel blocks until every drained
	// sandbox has received a best-effort kill attempt; kills are not
	// guaranteed, and any survivor expires server-side at IdleTimeout.
	// Force teardown skips the graceful drain of in-flight work
	// (Shutdown graceful=false stops the reconciler immediately); the idle
	// queue is retired store-side by PoolManager.Destroy below, which also
	// fences peers and tombstones the namespace.
	if force {
		slog.Info("force-deleting warm pool", "adapter", a.name, "warmPoolID", warmPoolID)
	}
	shutdownErr := entry.pool.Shutdown(ctx, !force)

	var destroyErr error
	var released int
	var releaseErr error
	if force && entry.destroyer != nil {
		// Store-level teardown: fences every peer sharing the state store
		// (no further replenishment), drains and kills idle sandboxes, and
		// tombstones the namespace so it cannot silently rebind. Retry-safe
		// per SDK contract; the tombstone expires after
		// forceDeleteTombstoneTTL.
		var res *opensandbox.PoolDestroyResult
		res, destroyErr = entry.destroyer.Destroy(ctx, warmPoolID, forceDestroyOptions())
		if destroyErr == nil {
			slog.Info("warm pool namespace destroyed",
				"adapter", a.name, "warmPoolID", warmPoolID,
				"drainedIdle", res.DrainedIdleCount, "killedIdle", res.KilledIdleCount)
		}
	} else {
		released, releaseErr = entry.pool.ReleaseAllIdleParallel(ctx, poolReleaseWorkers)
	}

	if shutdownErr != nil {
		return fmt.Errorf("shutting down warm pool %q: %w", warmPoolID, shutdownErr)
	}
	if destroyErr != nil {
		// PoolDestroyIncompleteError leaves the namespace DESTROYING; retrying
		// Destroy is safe per the SDK contract. Register the destroyer so a
		// repeated DELETE can resume it — the entry is already unregistered,
		// so without this the fence and any queued idle sandboxes would leak.
		a.poolsMu.Lock()
		if a.poolDestroyPending == nil {
			a.poolDestroyPending = make(map[string]poolDestroyer)
		}
		a.poolDestroyPending[warmPoolID] = entry.destroyer
		a.poolsMu.Unlock()
		slog.Warn("warm pool force destroy incomplete; namespace stays DESTROYING, retry DELETE to resume",
			"adapter", a.name, "warmPoolID", warmPoolID, "error", destroyErr)
		return fmt.Errorf("destroying warm pool %q: %w", warmPoolID, destroyErr)
	}
	if releaseErr != nil {
		return fmt.Errorf("releasing idle sandboxes of warm pool %q (%d released): %w", warmPoolID, released, releaseErr)
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
	// Hold opMu across the SDK Resize AND the targetSize publication: two
	// concurrent resizes could otherwise complete their SDK writes in one
	// order and record entry.targetSize in the opposite order, leaving
	// GetWarmPool reporting a size the SDK never ended on. The same lock
	// serializes against Delete's teardown.
	entry.opMu.Lock()
	defer entry.opMu.Unlock()
	if err := entry.pool.Resize(ctx, size); err != nil {
		return fmt.Errorf("resizing warm pool %q: %w", warmPoolID, err)
	}
	entry.targetSize = size
	return nil
}

// resolvePoolImageURI resolves templateID to a container image for pool
// creation. Unlike resolveImageURI (CreateSandbox path), it REFUSES to fall
// back to the raw template ID: pools pre-warm long-lived sandboxes, so the
// image must come from trusted server-side configuration (templateToImage or
// the template store), never from unconfigured request input.
func (a *Adapter) resolvePoolImageURI(ctx context.Context, templateID string) (string, error) {
	if mapped, ok := a.templateToImage[templateID]; ok {
		return mapped, nil
	}
	// Viper lowercases config map keys; retry lowercased before consulting
	// the template store.
	if mapped, ok := a.templateToImage[strings.ToLower(templateID)]; ok {
		return mapped, nil
	}
	if a.templates != nil {
		if entry, err := a.templates.GetTemplate(ctx, templateID); err == nil {
			return entry.ImageURI, nil
		} else if !errors.Is(err, ErrTemplateNotFound) {
			return "", fmt.Errorf("looking up template %q: %w", templateID, err)
		}
		if resolvedID, err := a.templates.ResolveAlias(ctx, templateID); err == nil {
			if entry, err := a.templates.GetTemplate(ctx, resolvedID); err == nil {
				return entry.ImageURI, nil
			}
		}
	}
	return "", fmt.Errorf("template %q is not mapped to an image (templateToImage) and not present in the template store; refusing to use the raw template ID as a pool image", templateID)
}

// lookupPoolByTemplate returns the pool serving templateID, or nil.
func (a *Adapter) lookupPoolByTemplate(templateID string) *poolEntry {
	a.poolsMu.RLock()
	defer a.poolsMu.RUnlock()
	// Map iteration order is random: when several pools serve the same
	// template, deterministically pick the oldest so CreateSandbox routing
	// does not jitter between calls.
	var best *poolEntry
	for _, entry := range a.pools {
		if entry.templateID != templateID {
			continue
		}
		if best == nil || entry.createdAt.Before(best.createdAt) {
			best = entry
		}
	}
	return best
}

// runPendingDestroy resumes a force delete whose Destroy failed on an
// earlier DELETE (the namespace stayed DESTROYING). Destroy is idempotent:
// on success the pending entry is consumed; on failure it stays registered
// so the operator can retry again.
func (a *Adapter) runPendingDestroy(ctx context.Context, warmPoolID string, destroyer poolDestroyer) error {
	res, err := destroyer.Destroy(ctx, warmPoolID, forceDestroyOptions())
	if err != nil {
		slog.Warn("warm pool pending destroy retry failed; namespace stays DESTROYING, retry DELETE again",
			"adapter", a.name, "warmPoolID", warmPoolID, "error", err)
		return fmt.Errorf("destroying warm pool %q: %w", warmPoolID, err)
	}
	a.poolsMu.Lock()
	delete(a.poolDestroyPending, warmPoolID)
	a.poolsMu.Unlock()
	slog.Info("pending warm pool destroy completed",
		"adapter", a.name, "warmPoolID", warmPoolID,
		"drainedIdle", res.DrainedIdleCount, "killedIdle", res.KilledIdleCount)
	return nil
}
