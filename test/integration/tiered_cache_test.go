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

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	mockadapter "github.com/e2bgateway/e2bgateway/internal/adapter/mock"
	"github.com/e2bgateway/e2bgateway/internal/api/dto"
	v1 "github.com/e2bgateway/e2bgateway/internal/api/v1"
	"github.com/e2bgateway/e2bgateway/internal/cache"
	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/routing"
	"github.com/e2bgateway/e2bgateway/internal/sessionstore"
)

// cacheReplica models one gateway pod: its own L1 + invalidator, shared
// Redis L2, and the SAME backend registry (both pods talk to one backend).
type cacheReplica struct {
	sbx  *cache.TieredCache[dto.SandboxInfo]
	tmpl *cache.TieredCache[[]*adapter.Template]
}

func newCacheReplica(t *testing.T, mr *miniredis.Miniredis) cacheReplica {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	inv := cache.NewInvalidator(client, "test:invalidate")
	mkOpts := func(name string) cache.TieredOptions {
		return cache.TieredOptions{
			Name: name, L1MaxSize: 100, L1TTL: time.Minute,
			// RedisCache.prefixed appends its own ":" — pass "test", not "test:".
			L2:          cache.NewRedisCache(cache.WrapRedisClient(client), "test", time.Minute),
			Invalidator: inv,
		}
	}
	r := cacheReplica{
		sbx:  cache.NewTiered[dto.SandboxInfo](mkOpts("sandbox_metadata")),
		tmpl: cache.NewTiered[[]*adapter.Template](mkOpts("template_list")),
	}
	inv.Register("sandbox_metadata", r.sbx.OnRemoteInvalidate)
	inv.Register("template_list", r.tmpl.OnRemoteInvalidate)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go inv.Run(ctx)
	select {
	case <-inv.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("invalidator subscriber not ready")
	}
	return r
}

func sandboxRequest(method, id string) *http.Request {
	req := httptest.NewRequest(method, "/sandboxes/"+id, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sandboxID", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// TestTieredCache_TwoReplicaSandboxConsistency: pause via replica A must be
// visible on replica B within 1s (issue #113 acceptance criterion).
//
// Pause (not Kill) is used deliberately: mock KillSandbox also deletes the
// sandbox→backend routing entry, and GetSandboxHandlerWithCache resolves the
// adapter before the cache lookup — a Kill-based test would see 404 on B via
// resolution alone, even with invalidation disabled. Pause keeps routing
// intact, so B can only observe "paused" if the Pub/Sub invalidation evicts
// its cached "running" entry (L1 TTL here is 1 minute).
func TestTieredCache_TwoReplicaSandboxConsistency(t *testing.T) {
	mr := miniredis.RunT(t)
	reg := adapter.NewRegistry(sessionstore.NewMemory(), time.Hour)
	require.NoError(t, reg.Register(mockadapter.New(nil)))
	router := routing.NewRouter(config.RoutingConfig{DefaultBackend: "mock", Strategy: "static"}, reg)
	a := newCacheReplica(t, mr)
	b := newCacheReplica(t, mr)

	// Create via replica A's handler (write-through).
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sandboxes", strings.NewReader(`{"templateID":"base","timeout":300}`))
	req.Header.Set("Content-Type", "application/json")
	v1.CreateSandboxHandlerWithCache(reg, router, "", a.sbx)(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	var created dto.SandboxCreateResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	id := created.SandboxID

	// Warm replica B's cache; it must hold the "running" state.
	w = httptest.NewRecorder()
	v1.GetSandboxHandlerWithCache(reg, router, b.sbx)(w, sandboxRequest(http.MethodGet, id))
	require.Equal(t, http.StatusOK, w.Code)
	var warm dto.SandboxInfo
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &warm))
	require.Equal(t, "running", warm.State)

	// Pause via replica A. Routing stays intact, so any state change B
	// observes must come from cache invalidation, not adapter resolution.
	w = httptest.NewRecorder()
	v1.PauseSandboxHandlerWithCache(reg, router, a.sbx)(w, sandboxRequest(http.MethodPost, id))
	require.Equal(t, http.StatusNoContent, w.Code)

	// Replica B must see state "paused" within 1s.
	require.Eventually(t, func() bool {
		w := httptest.NewRecorder()
		v1.GetSandboxHandlerWithCache(reg, router, b.sbx)(w, sandboxRequest(http.MethodGet, id))
		if w.Code != http.StatusOK {
			return false
		}
		var info dto.SandboxInfo
		if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
			return false
		}
		return info.State == "paused"
	}, time.Second, 10*time.Millisecond, "replica B must observe the paused state within 1s")
}

// TestTieredCache_TwoReplicaTemplateConsistency: template created via A is
// listed by B within 1s.
func TestTieredCache_TwoReplicaTemplateConsistency(t *testing.T) {
	mr := miniredis.RunT(t)
	reg := adapter.NewRegistry(sessionstore.NewMemory(), time.Hour)
	require.NoError(t, reg.Register(mockadapter.New(nil)))
	router := routing.NewRouter(config.RoutingConfig{DefaultBackend: "mock", Strategy: "static"}, reg)
	a := newCacheReplica(t, mr)
	b := newCacheReplica(t, mr)

	// listLen is safe to call from Eventually's condition goroutine: it
	// reports failure via the bool instead of require.* (FailNow must only
	// run on the test goroutine).
	listLen := func(r cacheReplica) (int, bool) {
		w := httptest.NewRecorder()
		v1.ListTemplatesHandlerWithCache(reg, router, r.tmpl)(w, httptest.NewRequest(http.MethodGet, "/templates", nil))
		if w.Code != http.StatusOK {
			return 0, false
		}
		var out []dto.TemplateInfo
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			return 0, false
		}
		return len(out), true
	}
	before, ok := listLen(b) // warm B
	require.True(t, ok, "warming replica B's template list must succeed")

	// Create via replica A's handler.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/templates", strings.NewReader(`{"name":"two-replica-tmpl"}`))
	req.Header.Set("Content-Type", "application/json")
	v1.CreateTemplateHandlerWithCache(reg, router, a.tmpl)(w, req)
	require.Equal(t, http.StatusAccepted, w.Code)

	require.Eventually(t, func() bool {
		n, ok := listLen(b)
		return ok && n == before+1
	}, time.Second, 10*time.Millisecond)
	n, ok := listLen(a)
	require.True(t, ok, "listing templates on replica A must succeed")
	assert.Equal(t, before+1, n)
}
