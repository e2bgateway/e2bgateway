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

package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/e2bgateway/e2bgateway/internal/api/dto"
	"github.com/e2bgateway/e2bgateway/internal/config"
)

func tieredTestConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Cache.Tiered.Enabled = true
	cfg.Cache.Tiered.L1 = config.TieredL1Config{MaxSize: 100, DefaultTTL: 30 * time.Second}
	cfg.Cache.Tiered.L2 = config.TieredL2Config{Timeout: 100 * time.Millisecond, KeyPrefix: "e2bgw:cache:", PubsubChannel: "e2bgw:cache:invalidate"}
	return cfg
}

func TestInitCaches_Disabled(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	require.NoError(t, s.initCaches())
	assert.Nil(t, s.sandboxCache)
	assert.Nil(t, s.templateCache)
	assert.Nil(t, s.healthCache)
	assert.Nil(t, s.invalidator)
}

func TestInitCaches_L1Only(t *testing.T) {
	s := &Server{cfg: tieredTestConfig()}
	require.NoError(t, s.initCaches())
	assert.NotNil(t, s.sandboxCache)
	assert.NotNil(t, s.templateCache)
	assert.NotNil(t, s.healthCache)
	assert.Nil(t, s.invalidator) // no l2.address → no Pub/Sub
}

func TestInitCaches_WithRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	cfg := tieredTestConfig()
	cfg.Cache.Tiered.L2.Address = mr.Addr() // loopback → plaintext OK
	s := &Server{cfg: cfg}
	require.NoError(t, s.initCaches())
	require.NotNil(t, s.invalidator)
	require.NotNil(t, s.cacheCancel)
	s.cacheCancel()
	<-s.cacheDone // Run must exit promptly on cancel
}

// Stop must close the tiered cache's Redis client after the listener drain
// so no connection pool outlives the server.
func TestInitCaches_StopClosesRedisClient(t *testing.T) {
	mr := miniredis.RunT(t)
	cfg := tieredTestConfig()
	cfg.Cache.Tiered.L2.Address = mr.Addr()
	s := &Server{cfg: cfg, httpServer: &http.Server{}}
	require.NoError(t, s.initCaches())
	require.NotNil(t, s.cacheRedisClient)
	require.NoError(t, s.Stop(context.Background()))
	assert.Error(t, s.cacheRedisClient.Ping(context.Background()).Err(), "client still usable after Stop")
}

// If the subscription is never acknowledged (Pub/Sub dials keep failing
// after the startup Ping), initCaches must tear the tiered caches down and
// serve pass-through rather than cache with an unsubscribed L1.
func TestInitCaches_SubscriptionNotReadyDegradesToPassThrough(t *testing.T) {
	oldTimeout := cacheReadyTimeout
	cacheReadyTimeout = 100 * time.Millisecond
	t.Cleanup(func() { cacheReadyTimeout = oldTimeout })

	mr := miniredis.RunT(t)
	// Proxy that forwards only the first connection (the startup Ping) and
	// closes every later one. Pub/Sub always dials a fresh connection, so the
	// subscription can never be acknowledged while Ping succeeds.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	var conns atomic.Int64
	go func() {
		for {
			c, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			if conns.Add(1) > 1 {
				_ = c.Close()
				continue
			}
			up, dialErr := net.Dial("tcp", mr.Addr())
			if dialErr != nil {
				_ = c.Close()
				continue
			}
			go func() { _, _ = io.Copy(c, up); _ = c.Close() }()
			go func() { _, _ = io.Copy(up, c); _ = up.Close() }()
		}
	}()
	cfg := tieredTestConfig()
	cfg.Cache.Tiered.L2.Address = ln.Addr().String()
	s := &Server{cfg: cfg}
	require.NoError(t, s.initCaches())

	assert.Nil(t, s.sandboxCache)
	assert.Nil(t, s.templateCache)
	assert.Nil(t, s.invalidator)
	assert.Nil(t, s.cacheCancel)
	assert.Nil(t, s.cacheDone)
	assert.Nil(t, s.cacheRedisClient)
	assert.NotNil(t, s.healthCache) // L1-only, no cross-replica semantics
}

// A stalled L2 connection must be bounded by cache.tiered.l2.timeout, not
// go-redis's default 3s socket read timeout (ContextTimeoutEnabled).
func TestInitCaches_L2TimeoutBoundsStalledReads(t *testing.T) {
	mr := miniredis.RunT(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	var conns atomic.Int64
	go func() {
		for {
			c, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			switch conns.Add(1) {
			case 1: // startup Ping: forward, then drop so later L2 ops dial fresh
				up, dialErr := net.Dial("tcp", mr.Addr())
				if dialErr != nil {
					_ = c.Close()
					continue
				}
				go func() { _, _ = io.Copy(c, up) }()
				go func() { _, _ = io.Copy(up, c) }()
				time.AfterFunc(100*time.Millisecond, func() { _ = c.Close(); _ = up.Close() })
			case 2: // Pub/Sub subscription: forward for the lifetime of the test
				up, dialErr := net.Dial("tcp", mr.Addr())
				if dialErr != nil {
					_ = c.Close()
					continue
				}
				go func() { _, _ = io.Copy(c, up); _ = c.Close() }()
				go func() { _, _ = io.Copy(up, c); _ = up.Close() }()
			default: // L2 commands: accept but never reply (stalled socket)
				go func() { _, _ = io.Copy(io.Discard, c) }()
			}
		}
	}()
	cfg := tieredTestConfig() // l2.timeout = 100ms
	cfg.Cache.Tiered.L2.Address = ln.Addr().String()
	s := &Server{cfg: cfg}
	require.NoError(t, s.initCaches())
	t.Cleanup(func() {
		s.cacheCancel()
		<-s.cacheDone
		_ = s.cacheRedisClient.Close()
	})
	time.Sleep(150 * time.Millisecond) // let the Ping connection drop

	start := time.Now()
	v, err := s.sandboxCache.GetOrLoad(context.Background(), "sb1", time.Minute,
		func(context.Context) (dto.SandboxInfo, error) { return dto.SandboxInfo{SandboxID: "sb1"}, nil })
	elapsed := time.Since(start)
	require.NoError(t, err) // L2 failure degrades, never fails the request
	assert.Equal(t, "sb1", v.SandboxID)
	assert.Less(t, elapsed, time.Second, "stalled L2 read not bounded by the configured timeout")
}

func TestInitCaches_UnreachableRedisFailsStartup(t *testing.T) {
	mr := miniredis.RunT(t)
	addr := mr.Addr()
	mr.Close()
	cfg := tieredTestConfig()
	cfg.Cache.Tiered.L2.Address = addr
	s := &Server{cfg: cfg}
	err := s.initCaches()
	assert.ErrorContains(t, err, "unreachable")
}
