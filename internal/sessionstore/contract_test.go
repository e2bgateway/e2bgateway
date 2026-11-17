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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storeFixture is one contract-suite run target. advance makes TTL expiry
// deterministic: memory tests sleep, miniredis tests FastForward.
type storeFixture struct {
	store   Store
	advance func(t *testing.T, d time.Duration)
}

// runStoreContract is the shared behavioral suite every Store implementation
// must pass. Errors and presence are asserted separately everywhere, so a
// store failure can never pass as a successful miss/expiry/deletion.
func runStoreContract(t *testing.T, fx storeFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("set get roundtrip", func(t *testing.T) {
		require.NoError(t, fx.store.Set(ctx, "k1", "v1", time.Minute))
		v, ok, err := fx.store.Get(ctx, "k1")
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, "v1", v)
	})

	t.Run("get miss", func(t *testing.T) {
		v, ok, err := fx.store.Get(ctx, "absent")
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Empty(t, v)
	})

	t.Run("delete", func(t *testing.T) {
		require.NoError(t, fx.store.Set(ctx, "k2", "v2", time.Minute))
		require.NoError(t, fx.store.Delete(ctx, "k2"))
		v, ok, err := fx.store.Get(ctx, "k2")
		require.NoError(t, err, "delete must leave a clean miss, not an error")
		assert.False(t, ok)
		assert.Empty(t, v)
		// deleting a missing key is not an error
		require.NoError(t, fx.store.Delete(ctx, "k2"))
	})

	t.Run("ttl expiry", func(t *testing.T) {
		require.NoError(t, fx.store.Set(ctx, "k3", "v3", 100*time.Millisecond))
		fx.advance(t, 200*time.Millisecond)
		v, ok, err := fx.store.Get(ctx, "k3")
		require.NoError(t, err, "expiry must surface as a clean miss, not an error")
		assert.False(t, ok)
		assert.Empty(t, v)
	})

	t.Run("no expiry when ttl <= 0", func(t *testing.T) {
		require.NoError(t, fx.store.Set(ctx, "k4", "v4", 0))
		fx.advance(t, time.Second)
		v, ok, err := fx.store.Get(ctx, "k4")
		require.NoError(t, err)
		assert.True(t, ok, "ttl<=0 key must not expire")
		assert.Equal(t, "v4", v)
	})

	t.Run("setnx arbitration", func(t *testing.T) {
		won, err := fx.store.SetNX(ctx, "k5", "first", time.Minute)
		require.NoError(t, err)
		assert.True(t, won)
		won, err = fx.store.SetNX(ctx, "k5", "second", time.Minute)
		require.NoError(t, err)
		assert.False(t, won)
		v, ok, err := fx.store.Get(ctx, "k5")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, "first", v, "SetNX loser must not overwrite the value")
	})

	t.Run("setnx after expiry wins", func(t *testing.T) {
		_, err := fx.store.SetNX(ctx, "k6", "old", 100*time.Millisecond)
		require.NoError(t, err)
		fx.advance(t, 200*time.Millisecond)
		won, err := fx.store.SetNX(ctx, "k6", "new", time.Minute)
		require.NoError(t, err)
		assert.True(t, won)
	})

	t.Run("touch refreshes ttl without rewriting value", func(t *testing.T) {
		require.NoError(t, fx.store.Set(ctx, "k7", "v7", 100*time.Millisecond))
		fx.advance(t, 60*time.Millisecond)
		require.NoError(t, fx.store.Touch(ctx, "k7", time.Hour))
		fx.advance(t, 100*time.Millisecond) // past the ORIGINAL expiry
		v, ok, err := fx.store.Get(ctx, "k7")
		require.NoError(t, err)
		assert.True(t, ok, "Touch must extend the entry's lifetime")
		assert.Equal(t, "v7", v)
	})

	t.Run("touch preserves sub-second ttl precision", func(t *testing.T) {
		// EXPIRE-style second truncation would drop the key at 1s, before the
		// requested 1.5s lifetime ends; PEXPIRE-style precision keeps it.
		require.NoError(t, fx.store.Set(ctx, "k9", "v9", time.Hour))
		require.NoError(t, fx.store.Touch(ctx, "k9", 1500*time.Millisecond))
		fx.advance(t, 1200*time.Millisecond)
		_, ok, err := fx.store.Get(ctx, "k9")
		require.NoError(t, err)
		assert.True(t, ok, "key must still be alive at 1.2s of a 1.5s TTL")
		fx.advance(t, 500*time.Millisecond) // 1.7s total > 1.5s
		_, ok, err = fx.store.Get(ctx, "k9")
		require.NoError(t, err)
		assert.False(t, ok, "key must expire after its full 1.5s TTL")
	})

	t.Run("touch missing key is a no-op that does not resurrect", func(t *testing.T) {
		require.NoError(t, fx.store.Touch(ctx, "k8-absent", time.Hour))
		v, ok, err := fx.store.Get(ctx, "k8-absent")
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Empty(t, v)

		// Deleted entries must not come back via Touch either.
		require.NoError(t, fx.store.Set(ctx, "k8", "v8", time.Minute))
		require.NoError(t, fx.store.Delete(ctx, "k8"))
		require.NoError(t, fx.store.Touch(ctx, "k8", time.Hour))
		_, ok, err = fx.store.Get(ctx, "k8")
		require.NoError(t, err)
		assert.False(t, ok, "Touch must not resurrect a deleted entry")
	})

	t.Run("scan visits prefix only", func(t *testing.T) {
		for k, v := range map[string]string{"p:a": "1", "p:b": "2", "q:c": "3"} {
			require.NoError(t, fx.store.Set(ctx, k, v, time.Minute))
		}
		seen := map[string]string{}
		require.NoError(t, fx.store.Scan(ctx, "p:", func(k, v string) { seen[k] = v }))
		assert.Len(t, seen, 2)
		assert.Equal(t, "1", seen["p:a"])
		assert.Equal(t, "2", seen["p:b"])
	})
}
