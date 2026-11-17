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

package sessionstore

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/e2bgateway/e2bgateway/internal/config"
)

func miniredisFixture(t *testing.T) (storeFixture, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return storeFixture{
		store:   NewRedisWithClient(client, "test:"),
		advance: func(t *testing.T, d time.Duration) { mr.FastForward(d) },
	}, mr
}

func TestRedisStoreContract(t *testing.T) {
	fx, _ := miniredisFixture(t)
	runStoreContract(t, fx)
}

func TestRedisStoreKeyPrefix(t *testing.T) {
	fx, mr := miniredisFixture(t)
	ctx := context.Background()
	if err := fx.store.Set(ctx, "token:sb1", "v", time.Minute); err != nil {
		t.Fatal(err)
	}
	if !mr.Exists("test:token:sb1") {
		t.Error("expected physical key to carry the configured prefix")
	}
}

func TestRedisStoreErrors(t *testing.T) {
	fx, mr := miniredisFixture(t)
	mr.Close()
	ctx := context.Background()
	if _, _, err := fx.store.Get(ctx, "x"); err == nil {
		t.Error("expected error from closed redis")
	}
	if err := fx.store.Set(ctx, "x", "v", time.Minute); err == nil {
		t.Error("expected error from closed redis")
	}
}

func TestNewMemoryDispatch(t *testing.T) {
	s, err := New(config.SessionStoreConfig{Type: "memory"})
	if err != nil || s == nil {
		t.Fatalf("New(memory): (%v, %v)", s, err)
	}
}

func TestNewRedisRequiresReachableServer(t *testing.T) {
	// Port 1 on loopback: connection refused ⇒ New must fail (fail fast).
	_, err := New(config.SessionStoreConfig{Type: "redis", Addr: "127.0.0.1:1", KeyPrefix: "p:"})
	if err == nil {
		t.Fatal("expected error for unreachable redis")
	}
}

// A key prefix containing glob metacharacters must match literally in Scan
// (Redis MATCH treats the pattern as a glob).
func TestRedisStoreScanEscapesGlobPrefix(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	store := NewRedisWithClient(client, "tenant[1]:")
	ctx := context.Background()
	require.NoError(t, store.Set(ctx, "routing:a", "backendA", time.Minute))
	// A key that an UNESCAPED pattern "tenant[1]:routing:*" would also match
	// ("[1]" as a character class matches "1"): it must NOT be visited.
	require.NoError(t, client.Set(ctx, "tenant1:routing:b", "backendB", time.Minute).Err())

	seen := map[string]string{}
	require.NoError(t, store.Scan(ctx, "routing:", func(k, v string) { seen[k] = v }))
	require.Len(t, seen, 1)
	assert.Equal(t, "backendA", seen["routing:a"])
}
