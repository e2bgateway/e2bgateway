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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/e2bgateway/e2bgateway/internal/metrics"
)

// invalidateMsg is the Pub/Sub payload. It carries only names/keys, never
// cached values. Sender lets each replica skip its own broadcasts.
type invalidateMsg struct {
	Cache  string `json:"cache"`
	Key    string `json:"key"`
	Sender string `json:"sender"`
}

// Invalidator broadcasts cache invalidations over Redis Pub/Sub and applies
// remote invalidations to registered caches. One per process; Run must be
// started in a goroutine.
type Invalidator struct {
	client     redis.UniversalClient
	channel    string
	instanceID string

	mu       sync.RWMutex
	handlers map[string]func(string)

	ready     chan struct{}
	readyOnce sync.Once
}

// NewInvalidator creates an invalidator. instanceID = hostname + random
// suffix so replicas (and tests in one process) never collide.
func NewInvalidator(client redis.UniversalClient, channel string) *Invalidator {
	host, _ := os.Hostname()
	var b [8]byte
	_, _ = rand.Read(b[:])
	return &Invalidator{
		client:     client,
		channel:    channel,
		instanceID: host + "-" + hex.EncodeToString(b[:]),
		handlers:   make(map[string]func(string)),
		ready:      make(chan struct{}),
	}
}

// Register attaches a cache's OnRemoteInvalidate under its metrics name.
func (inv *Invalidator) Register(name string, fn func(string)) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	inv.handlers[name] = fn
}

// Ready is closed once the subscription is first established. Callers that
// need broadcast delivery guarantees (tests, startup sequencing) wait on it.
func (inv *Invalidator) Ready() <-chan struct{} { return inv.ready }

// Broadcast publishes an invalidation. Failures are logged + metered, never
// propagated (implements Broadcaster).
func (inv *Invalidator) Broadcast(ctx context.Context, cacheName, key string) {
	raw, err := json.Marshal(invalidateMsg{Cache: cacheName, Key: key, Sender: inv.instanceID})
	if err != nil {
		return // static struct; cannot fail in practice
	}
	if err := inv.client.Publish(ctx, inv.channel, raw).Err(); err != nil {
		metrics.RecordCache(cacheName, "l2_error")
		slog.Warn("cache invalidation broadcast failed", "cache", cacheName, "key", key, "error", err)
	}
}

// Run consumes invalidation broadcasts until ctx is cancelled. go-redis
// reconnects the subscription internally; if the channel still closes, an
// outer loop re-subscribes with exponential backoff (1s → 30s). While
// disconnected, coherence degrades to L1 TTL expiry (bounded staleness).
func (inv *Invalidator) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		sub := inv.client.Subscribe(ctx, inv.channel)
		// Ping only writes the command; it never reads a reply, so it cannot
		// confirm the subscription. Read the first message instead and require
		// the subscribe acknowledgement before declaring readiness.
		msg, err := sub.Receive(ctx)
		if err != nil {
			_ = sub.Close()
			if ctx.Err() != nil {
				return
			}
			slog.Warn("cache invalidation subscription failed; reconnecting",
				"channel", inv.channel, "backoff", backoff, "error", err)
		} else {
			if _, ok := msg.(*redis.Subscription); ok {
				inv.readyOnce.Do(func() { close(inv.ready) })
			}
			ch := sub.Channel()
			dropped := false
			for !dropped && ctx.Err() == nil {
				select {
				case <-ctx.Done():
				case m, ok := <-ch:
					if !ok {
						dropped = true
						break
					}
					backoff = time.Second
					// A delivered message proves the subscription is live even if
					// the startup ack was missed; Ready() must not stay open forever.
					inv.readyOnce.Do(func() { close(inv.ready) })
					inv.handle(m.Payload)
				}
			}
			_ = sub.Close()
			if ctx.Err() != nil {
				return
			}
			slog.Warn("cache invalidation subscription dropped; reconnecting",
				"channel", inv.channel, "backoff", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (inv *Invalidator) handle(payload string) {
	var msg invalidateMsg
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		return
	}
	if msg.Sender == inv.instanceID {
		return
	}
	inv.mu.RLock()
	fn := inv.handlers[msg.Cache]
	inv.mu.RUnlock()
	if fn != nil {
		fn(msg.Key)
	}
}
