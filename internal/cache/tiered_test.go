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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTieredForTest[T any](t *testing.T, mr *miniredis.Miniredis, name string) *TieredCache[T] {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewTiered[T](TieredOptions{
		Name: name, L1MaxSize: 100, L1TTL: time.Minute,
		L2: NewRedisCache(WrapRedisClient(client), "test", time.Minute),
	})
}

func strLoader(v string, calls *atomic.Int64) func(context.Context) (string, error) {
	return func(context.Context) (string, error) {
		if calls != nil {
			calls.Add(1)
		}
		return v, nil
	}
}

func TestTiered_L1Hit(t *testing.T) {
	mr := miniredis.RunT(t)
	tc := newTieredForTest[string](t, mr, "test")
	var calls atomic.Int64
	ctx := context.Background()

	for range 3 {
		v, err := tc.GetOrLoad(ctx, "k", time.Minute, strLoader("v1", &calls))
		require.NoError(t, err)
		assert.Equal(t, "v1", v)
	}
	assert.Equal(t, int64(1), calls.Load())
}

func TestTiered_L2HitBackfillsL1(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newTieredForTest[string](t, mr, "test")
	b := newTieredForTest[string](t, mr, "test") // fresh L1, shared L2
	ctx := context.Background()

	_, err := a.GetOrLoad(ctx, "k", time.Minute, strLoader("v1", nil))
	require.NoError(t, err)

	var calls atomic.Int64
	v, err := b.GetOrLoad(ctx, "k", time.Minute, strLoader("SHOULD-NOT-LOAD", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v1", v)
	assert.Equal(t, int64(0), calls.Load())
	// backfilled into b's L1: delete L2, b still hits L1
	mr.FlushAll()
	v, err = b.GetOrLoad(ctx, "k", time.Minute, strLoader("SHOULD-NOT-LOAD", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v1", v)
}

func TestTiered_MissFillsBothTiers(t *testing.T) {
	mr := miniredis.RunT(t)
	tc := newTieredForTest[string](t, mr, "test")
	ctx := context.Background()

	_, err := tc.GetOrLoad(ctx, "k", time.Minute, strLoader("v1", nil))
	require.NoError(t, err)

	raw, hit, err := tc.l2.Get(ctx, "k")
	require.NoError(t, err)
	require.True(t, hit)
	assert.JSONEq(t, `"v1"`, raw)
	_, ok := tc.l1.Get("k")
	assert.True(t, ok)
}

func TestTiered_SetWriteThrough(t *testing.T) {
	mr := miniredis.RunT(t)
	tc := newTieredForTest[string](t, mr, "test")
	ctx := context.Background()

	tc.Set(ctx, "k", "v2", time.Minute)

	raw, hit, err := tc.l2.Get(ctx, "k")
	require.NoError(t, err)
	assert.True(t, hit)
	assert.JSONEq(t, `"v2"`, raw)
	var calls atomic.Int64
	v, err := tc.GetOrLoad(ctx, "k", time.Minute, strLoader("NO", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v2", v)
	assert.Equal(t, int64(0), calls.Load())
}

func TestTiered_Invalidate(t *testing.T) {
	mr := miniredis.RunT(t)
	tc := newTieredForTest[string](t, mr, "test")
	ctx := context.Background()

	_, err := tc.GetOrLoad(ctx, "k", time.Minute, strLoader("v1", nil))
	require.NoError(t, err)
	tc.Invalidate(ctx, "k")

	_, ok := tc.l1.Get("k")
	assert.False(t, ok)
	_, hit, err := tc.l2.Get(ctx, "k")
	require.NoError(t, err)
	assert.False(t, hit)
}

func TestTiered_TTLExpiry(t *testing.T) {
	mr := miniredis.RunT(t)
	tc := newTieredForTest[string](t, mr, "test")
	var calls atomic.Int64
	ctx := context.Background()

	_, err := tc.GetOrLoad(ctx, "k", 20*time.Millisecond, strLoader("v1", &calls))
	require.NoError(t, err)
	time.Sleep(40 * time.Millisecond)
	mr.FastForward(40 * time.Millisecond)
	_, err = tc.GetOrLoad(ctx, "k", 20*time.Millisecond, strLoader("v1", &calls))
	require.NoError(t, err)
	assert.Equal(t, int64(2), calls.Load())
}

func TestTiered_L2DownDegrades(t *testing.T) {
	mr := miniredis.RunT(t)
	tc := newTieredForTest[string](t, mr, "test")
	ctx := context.Background()
	_, err := tc.GetOrLoad(ctx, "k", time.Minute, strLoader("v1", nil))
	require.NoError(t, err)

	mr.Close() // L2 unreachable

	var calls atomic.Int64
	v, err := tc.GetOrLoad(ctx, "k2", time.Minute, strLoader("v2", &calls))
	require.NoError(t, err) // request must NOT fail
	assert.Equal(t, "v2", v)
	assert.Equal(t, int64(1), calls.Load())
	tc.Set(ctx, "k3", "v3", time.Minute) // must not panic or block
	tc.Invalidate(ctx, "k")              // must not panic or block
}

func TestTiered_BadJSONFallsThrough(t *testing.T) {
	mr := miniredis.RunT(t)
	tc := newTieredForTest[string](t, mr, "test")
	ctx := context.Background()
	require.NoError(t, mr.Set("test:k", "not-json"))

	var calls atomic.Int64
	v, err := tc.GetOrLoad(ctx, "k", time.Minute, strLoader("v1", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v1", v)
	assert.Equal(t, int64(1), calls.Load())
}

func TestTiered_Singleflight(t *testing.T) {
	mr := miniredis.RunT(t)
	tc := newTieredForTest[string](t, mr, "test")
	var calls atomic.Int64
	loader := func(context.Context) (string, error) {
		calls.Add(1)
		time.Sleep(50 * time.Millisecond)
		return "v1", nil
	}
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			v, err := tc.GetOrLoad(context.Background(), "k", time.Minute, loader)
			assert.NoError(t, err)
			assert.Equal(t, "v1", v)
		})
	}
	wg.Wait()
	assert.Equal(t, int64(1), calls.Load())
}

// The leader's ctx cancelling mid-load must not fail a coalesced waiter
// whose own ctx is still live: the load runs detached and delivers to all.
func TestTiered_SingleflightLeaderCancelDoesNotFailWaiters(t *testing.T) {
	mr := miniredis.RunT(t)
	tc := newTieredForTest[string](t, mr, "test")
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	loader := func(context.Context) (string, error) {
		calls.Add(1)
		close(started)
		<-release
		return "v1", nil
	}
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	t.Cleanup(cancelLeader)
	leaderErr := make(chan error, 1)
	go func() {
		_, err := tc.GetOrLoad(leaderCtx, "k", time.Minute, loader)
		leaderErr <- err
	}()
	<-started

	type result struct {
		v   string
		err error
	}
	waiterDone := make(chan result, 1)
	go func() {
		v, err := tc.GetOrLoad(context.Background(), "k", time.Minute, strLoader("SHOULD-NOT-LOAD", nil))
		waiterDone <- result{v, err}
	}()
	time.Sleep(10 * time.Millisecond) // let the waiter join the flight
	cancelLeader()
	require.ErrorIs(t, <-leaderErr, context.Canceled)

	close(release)
	res := <-waiterDone
	require.NoError(t, res.err)
	assert.Equal(t, "v1", res.v)
	assert.Equal(t, int64(1), calls.Load())
}

// A waiter whose own ctx cancels returns ctx.Err() promptly while the load
// continues for the remaining callers.
func TestTiered_SingleflightWaiterCancelReturnsPromptly(t *testing.T) {
	mr := miniredis.RunT(t)
	tc := newTieredForTest[string](t, mr, "test")
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	loader := func(context.Context) (string, error) {
		calls.Add(1)
		close(started)
		<-release
		return "v1", nil
	}
	type result struct {
		v   string
		err error
	}
	leaderDone := make(chan result, 1)
	go func() {
		v, err := tc.GetOrLoad(context.Background(), "k", time.Minute, loader)
		leaderDone <- result{v, err}
	}()
	<-started

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	t.Cleanup(cancelWaiter)
	waiterErr := make(chan error, 1)
	go func() {
		_, err := tc.GetOrLoad(waiterCtx, "k", time.Minute, strLoader("SHOULD-NOT-LOAD", nil))
		waiterErr <- err
	}()
	time.Sleep(10 * time.Millisecond) // let the waiter join the flight
	cancelWaiter()
	select {
	case err := <-waiterErr:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter did not return promptly")
	}

	close(release)
	res := <-leaderDone
	require.NoError(t, res.err)
	assert.Equal(t, "v1", res.v)
	assert.Equal(t, int64(1), calls.Load())
}

func TestTiered_L1OnlyMode(t *testing.T) {
	tc := NewTiered[string](TieredOptions{Name: "test", L1MaxSize: 10, L1TTL: time.Minute})
	var calls atomic.Int64
	v, err := tc.GetOrLoad(context.Background(), "k", time.Minute, strLoader("v1", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v1", v)
	v, err = tc.GetOrLoad(context.Background(), "k", time.Minute, strLoader("NO", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v1", v)
	assert.Equal(t, int64(1), calls.Load())
	tc.Set(context.Background(), "k", "v2", time.Minute)
	tc.Invalidate(context.Background(), "k")
}
