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
)

// storeFixture is one contract-suite run target. advance makes TTL expiry
// deterministic: memory tests sleep, miniredis tests FastForward.
type storeFixture struct {
	store   Store
	advance func(t *testing.T, d time.Duration)
}

// runStoreContract is the shared behavioral suite every Store implementation
// must pass.
func runStoreContract(t *testing.T, fx storeFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("set get roundtrip", func(t *testing.T) {
		if err := fx.store.Set(ctx, "k1", "v1", time.Minute); err != nil {
			t.Fatal(err)
		}
		v, ok, err := fx.store.Get(ctx, "k1")
		if err != nil || !ok || v != "v1" {
			t.Fatalf("got (%q,%v,%v), want (v1,true,nil)", v, ok, err)
		}
	})

	t.Run("get miss", func(t *testing.T) {
		v, ok, err := fx.store.Get(ctx, "absent")
		if err != nil || ok || v != "" {
			t.Fatalf("got (%q,%v,%v), want (\"\",false,nil)", v, ok, err)
		}
	})

	t.Run("delete", func(t *testing.T) {
		if err := fx.store.Set(ctx, "k2", "v2", time.Minute); err != nil {
			t.Fatal(err)
		}
		if err := fx.store.Delete(ctx, "k2"); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := fx.store.Get(ctx, "k2"); ok {
			t.Fatal("key still present after delete")
		}
		// deleting a missing key is not an error
		if err := fx.store.Delete(ctx, "k2"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("ttl expiry", func(t *testing.T) {
		if err := fx.store.Set(ctx, "k3", "v3", 100*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		fx.advance(t, 200*time.Millisecond)
		if _, ok, _ := fx.store.Get(ctx, "k3"); ok {
			t.Fatal("expired key still visible")
		}
	})

	t.Run("no expiry when ttl <= 0", func(t *testing.T) {
		if err := fx.store.Set(ctx, "k4", "v4", 0); err != nil {
			t.Fatal(err)
		}
		fx.advance(t, time.Second)
		if v, ok, _ := fx.store.Get(ctx, "k4"); !ok || v != "v4" {
			t.Fatal("ttl<=0 key must not expire")
		}
	})

	t.Run("setnx arbitration", func(t *testing.T) {
		won, err := fx.store.SetNX(ctx, "k5", "first", time.Minute)
		if err != nil || !won {
			t.Fatalf("first SetNX: (%v,%v), want (true,nil)", won, err)
		}
		won, err = fx.store.SetNX(ctx, "k5", "second", time.Minute)
		if err != nil || won {
			t.Fatalf("second SetNX: (%v,%v), want (false,nil)", won, err)
		}
		if v, _, _ := fx.store.Get(ctx, "k5"); v != "first" {
			t.Fatalf("SetNX loser overwrote value: %q", v)
		}
	})

	t.Run("setnx after expiry wins", func(t *testing.T) {
		if _, err := fx.store.SetNX(ctx, "k6", "old", 100*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		fx.advance(t, 200*time.Millisecond)
		won, err := fx.store.SetNX(ctx, "k6", "new", time.Minute)
		if err != nil || !won {
			t.Fatalf("SetNX after expiry: (%v,%v), want (true,nil)", won, err)
		}
	})

	t.Run("scan visits prefix only", func(t *testing.T) {
		for k, v := range map[string]string{"p:a": "1", "p:b": "2", "q:c": "3"} {
			if err := fx.store.Set(ctx, k, v, time.Minute); err != nil {
				t.Fatal(err)
			}
		}
		seen := map[string]string{}
		if err := fx.store.Scan(ctx, "p:", func(k, v string) { seen[k] = v }); err != nil {
			t.Fatal(err)
		}
		if len(seen) != 2 || seen["p:a"] != "1" || seen["p:b"] != "2" {
			t.Fatalf("scan result: %v", seen)
		}
	})
}
