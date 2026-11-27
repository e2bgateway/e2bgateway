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
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// replica bundles one cache + invalidator, as a gateway pod would have.
type replica struct {
	tc  *TieredCache[string]
	inv *Invalidator
}

func newReplica(t *testing.T, mr *miniredis.Miniredis) replica {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	inv := NewInvalidator(client, "test:invalidate")
	tc := NewTiered[string](TieredOptions{
		Name: "template_list", L1MaxSize: 100, L1TTL: time.Minute,
		L2:          NewRedisCache(WrapRedisClient(client), "test", time.Minute),
		Invalidator: inv,
	})
	inv.Register("template_list", tc.OnRemoteInvalidate)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go inv.Run(ctx)
	select {
	case <-inv.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("invalidator subscriber not ready")
	}
	return replica{tc: tc, inv: inv}
}

func TestInvalidator_SetBroadcastClearsRemoteL1(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newReplica(t, mr)
	b := newReplica(t, mr)
	ctx := context.Background()

	// Warm both L1s with v1.
	for _, r := range []replica{a, b} {
		_, err := r.tc.GetOrLoad(ctx, "tmpl:mock", time.Minute, strLoader("v1", nil))
		require.NoError(t, err)
	}

	// A writes v2 (write-through + broadcast).
	a.tc.Set(ctx, "tmpl:mock", "v2", time.Minute)

	// B sees v2 within 1s (issue #113 acceptance).
	require.Eventually(t, func() bool {
		v, err := b.tc.GetOrLoad(ctx, "tmpl:mock", time.Minute, strLoader("SHOULD-NOT-LOAD", nil))
		return err == nil && v == "v2"
	}, time.Second, 10*time.Millisecond)

	// A's own L1 must NOT be cleared by its own broadcast.
	v, err := a.tc.GetOrLoad(ctx, "tmpl:mock", time.Minute, strLoader("NO", nil))
	require.NoError(t, err)
	assert.Equal(t, "v2", v)
}

func TestInvalidator_InvalidateBroadcast(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newReplica(t, mr)
	b := newReplica(t, mr)
	ctx := context.Background()

	for _, r := range []replica{a, b} {
		_, err := r.tc.GetOrLoad(ctx, "tmpl:mock", time.Minute, strLoader("v1", nil))
		require.NoError(t, err)
	}

	a.tc.Invalidate(ctx, "tmpl:mock")

	// B's next read must go to the loader (L1 cleared, L2 deleted).
	require.Eventually(t, func() bool {
		v, err := b.tc.GetOrLoad(ctx, "tmpl:mock", time.Minute, strLoader("v3", nil))
		return err == nil && v == "v3"
	}, time.Second, 10*time.Millisecond)
}

// If Redis is briefly unreachable at startup, the first sub.Ping fails but
// go-redis's Channel reconnects internally once Redis returns. Ready() must
// still close on the first delivered message, not stay open forever.
func TestInvalidator_ReadyClosesOnFirstMessageAfterPingFailure(t *testing.T) {
	mr := miniredis.RunT(t)
	var up atomic.Bool
	var dials atomic.Int64
	client := redis.NewClient(&redis.Options{
		Addr:       mr.Addr(),
		MaxRetries: -1, // Ping must fail fast while "down", not retry past the outage
		Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			if !up.Load() {
				return nil, errors.New("transient: redis unreachable")
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	})
	t.Cleanup(func() { _ = client.Close() })
	inv := NewInvalidator(client, "test:invalidate")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go inv.Run(ctx)

	// Wait until Run has attempted (and failed) the startup connection, so
	// the first sub.Ping is guaranteed to have failed before Redis comes up.
	require.Eventually(t, func() bool { return dials.Load() > 0 }, 2*time.Second, 5*time.Millisecond)
	up.Store(true)

	// Publish repeatedly until the internal reconnect delivers one; Ready()
	// must close even though no outer-loop Ping ever succeeded.
	pub := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = pub.Close() })
	require.Eventually(t, func() bool {
		pub.Publish(ctx, "test:invalidate", `{"cache":"template_list","key":"k","sender":"other"}`)
		select {
		case <-inv.Ready():
			return true
		default:
			return false
		}
	}, 5*time.Second, 50*time.Millisecond, "Ready() not closed after first delivered message")
}

func TestInvalidator_UnknownCacheIgnored(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newReplica(t, mr)
	// Broadcast for an unregistered cache name must not panic.
	a.inv.Broadcast(context.Background(), "no_such_cache", "k")
	time.Sleep(50 * time.Millisecond)
}
