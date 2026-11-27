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

package v1_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/api/dto"
	v1 "github.com/e2bgateway/e2bgateway/internal/api/v1"
	"github.com/e2bgateway/e2bgateway/internal/cache"
)

func newSandboxCacheForTest() *cache.TieredCache[dto.SandboxInfo] {
	return cache.NewTiered[dto.SandboxInfo](cache.TieredOptions{
		Name: "sandbox_metadata", L1MaxSize: 100, L1TTL: time.Minute,
	})
}

func sandboxReq(method, target, id, body string) *http.Request {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	if id != "" {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("sandboxID", id)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	}
	return req
}

func TestGetSandboxHandler_CacheHitAndInvalidation(t *testing.T) {
	reg, r := setupTestRouter()
	sbxCache := newSandboxCacheForTest()

	// Create a sandbox.
	w := httptest.NewRecorder()
	v1.CreateSandboxHandler(reg, r, "")(w, sandboxReq(http.MethodPost, "/sandboxes", "", `{"templateID":"base","timeout":300}`))
	require.Equal(t, http.StatusCreated, w.Code)
	var created dto.SandboxCreateResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	id := created.SandboxID

	// First GET populates the cache.
	w = httptest.NewRecorder()
	v1.GetSandboxHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodGet, "/sandboxes/"+id, id, ""))
	require.Equal(t, http.StatusOK, w.Code)

	// Pause DIRECTLY through the adapter (bypassing handler invalidation):
	// the cached GET must still report "running" — proves the read was served
	// from cache. A direct kill cannot be used here: it deletes the
	// sandbox→backend routing entry, so resolveAdapterForSandbox would 404
	// before the cache is ever consulted.
	a := reg.List()[0]
	require.NoError(t, a.PauseSandbox(context.Background(), id))
	w = httptest.NewRecorder()
	v1.GetSandboxHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodGet, "/sandboxes/"+id, id, ""))
	require.Equal(t, http.StatusOK, w.Code)
	var cached dto.SandboxInfo
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &cached))
	assert.Equal(t, "running", cached.State)

	// Uncached handler sees the truth: "paused".
	w = httptest.NewRecorder()
	v1.GetSandboxHandler(reg, r)(w, sandboxReq(http.MethodGet, "/sandboxes/"+id, id, ""))
	require.Equal(t, http.StatusOK, w.Code)
	var fresh dto.SandboxInfo
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &fresh))
	assert.Equal(t, "paused", fresh.State)
}

func TestKillSandboxHandler_InvalidatesCache(t *testing.T) {
	reg, r := setupTestRouter()
	sbxCache := newSandboxCacheForTest()

	w := httptest.NewRecorder()
	v1.CreateSandboxHandlerWithCache(reg, r, "", sbxCache)(w, sandboxReq(http.MethodPost, "/sandboxes", "", `{"templateID":"base","timeout":300}`))
	require.Equal(t, http.StatusCreated, w.Code)
	var created dto.SandboxCreateResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	id := created.SandboxID

	// Warm the cache.
	w = httptest.NewRecorder()
	v1.GetSandboxHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodGet, "/sandboxes/"+id, id, ""))
	require.Equal(t, http.StatusOK, w.Code)

	// Kill through the cache-aware handler → invalidation.
	w = httptest.NewRecorder()
	v1.KillSandboxHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodDelete, "/sandboxes/"+id, id, ""))
	require.Equal(t, http.StatusNoContent, w.Code)

	// Next cached GET must miss and 404.
	w = httptest.NewRecorder()
	v1.GetSandboxHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodGet, "/sandboxes/"+id, id, ""))
	assert.Equal(t, http.StatusNotFound, w.Code)

	// Prove the invalidation at the cache level, bypassing the resolver: the
	// 404 above alone would also occur without any Invalidate call, because
	// resolution fails first once the routing entry is gone. The L1 entry
	// must be cleared, so GetOrLoad has to invoke the loader.
	loads := 0
	_, err := sbxCache.GetOrLoad(context.Background(), "sbx:mock:"+id, time.Minute, func(context.Context) (dto.SandboxInfo, error) {
		loads++
		return dto.SandboxInfo{}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, loads, "KillSandboxHandlerWithCache must invalidate the cached metadata")
}

func TestPauseResumeSetTimeout_InvalidateCache(t *testing.T) {
	reg, r := setupTestRouter()
	sbxCache := newSandboxCacheForTest()

	w := httptest.NewRecorder()
	v1.CreateSandboxHandler(reg, r, "")(w, sandboxReq(http.MethodPost, "/sandboxes", "", `{"templateID":"base","timeout":300}`))
	require.Equal(t, http.StatusCreated, w.Code)
	var created dto.SandboxCreateResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	id := created.SandboxID

	warm := func() {
		w := httptest.NewRecorder()
		v1.GetSandboxHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodGet, "/sandboxes/"+id, id, ""))
		require.Equal(t, http.StatusOK, w.Code)
	}

	// Pause invalidates → state reflects "paused" on next read.
	warm()
	w = httptest.NewRecorder()
	v1.PauseSandboxHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodPost, "/sandboxes/"+id+"/pause", id, ""))
	require.Equal(t, http.StatusNoContent, w.Code)
	w = httptest.NewRecorder()
	v1.GetSandboxHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodGet, "/sandboxes/"+id, id, ""))
	require.Equal(t, http.StatusOK, w.Code)
	var info dto.SandboxInfo
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &info))
	assert.Equal(t, "paused", info.State)

	// Resume write-through → state "running" visible immediately.
	w = httptest.NewRecorder()
	v1.ResumeSandboxHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodPost, "/sandboxes/"+id+"/resume", id, ""))
	require.Equal(t, http.StatusOK, w.Code)
	w = httptest.NewRecorder()
	v1.GetSandboxHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodGet, "/sandboxes/"+id, id, ""))
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &info))
	assert.Equal(t, "running", info.State)

	// SetTimeout invalidates (EndAt is part of cached metadata).
	warm()
	w = httptest.NewRecorder()
	v1.SetTimeoutHandlerWithCache(reg, r, sbxCache)(w, sandboxReq(http.MethodPatch, "/sandboxes/"+id+"/timeout", id, `{"timeout":600}`))
	require.Equal(t, http.StatusNoContent, w.Code)

	// Cache-level proof, bypassing the handler: the entry must be gone (the
	// loader runs) and the reloaded metadata must carry the extended EndAt
	// (~now+600s). A 204 alone cannot detect a missing Invalidate call.
	loads := 0
	a := reg.List()[0]
	loaded, err := sbxCache.GetOrLoad(context.Background(), "sbx:mock:"+id, time.Minute, func(ctx context.Context) (dto.SandboxInfo, error) {
		loads++
		sb, err := a.GetSandbox(ctx, id)
		if err != nil {
			return dto.SandboxInfo{}, err
		}
		return dto.SandboxInfo{SandboxID: sb.SandboxID, EndAt: sb.EndAt}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, loads, "SetTimeoutHandlerWithCache must invalidate the cached metadata")
	assert.True(t, loaded.EndAt.After(time.Now().Add(500*time.Second)), "reloaded metadata must carry the extended EndAt, got %s", loaded.EndAt)
}

func newTemplateCacheForTest() *cache.TieredCache[[]*adapter.Template] {
	return cache.NewTiered[[]*adapter.Template](cache.TieredOptions{
		Name: "template_list", L1MaxSize: 100, L1TTL: time.Minute,
	})
}

func TestListTemplatesHandler_CacheHit(t *testing.T) {
	reg, r := setupTestRouter()
	tmplCache := newTemplateCacheForTest()

	list := func() []dto.TemplateInfo {
		w := httptest.NewRecorder()
		v1.ListTemplatesHandlerWithCache(reg, r, tmplCache)(w, httptest.NewRequest(http.MethodGet, "/templates", nil))
		require.Equal(t, http.StatusOK, w.Code)
		var out []dto.TemplateInfo
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		return out
	}
	before := list()

	// Create a template DIRECTLY through the adapter (bypass invalidation).
	a := reg.List()[0]
	_, err := a.CreateTemplate(context.Background(), &adapter.CreateTemplateRequest{Name: "cache-test-tmpl"})
	require.NoError(t, err)

	// Cached list must be unchanged (proves cache hit).
	assert.Len(t, list(), len(before))

	// Uncached list sees the new template.
	w := httptest.NewRecorder()
	v1.ListTemplatesHandler(reg, r)(w, httptest.NewRequest(http.MethodGet, "/templates", nil))
	var fresh []dto.TemplateInfo
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &fresh))
	assert.Len(t, fresh, len(before)+1)
}

func TestCreateTemplateHandler_InvalidatesCache(t *testing.T) {
	reg, r := setupTestRouter()
	tmplCache := newTemplateCacheForTest()

	list := func() int {
		w := httptest.NewRecorder()
		v1.ListTemplatesHandlerWithCache(reg, r, tmplCache)(w, httptest.NewRequest(http.MethodGet, "/templates", nil))
		require.Equal(t, http.StatusOK, w.Code)
		var out []dto.TemplateInfo
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		return len(out)
	}
	before := list()

	// Create through the cache-aware handler → invalidation broadcast path.
	w := httptest.NewRecorder()
	v1.CreateTemplateHandlerWithCache(reg, r, tmplCache)(w, sandboxReq(http.MethodPost, "/templates", "", `{"name":"cache-test-tmpl2"}`))
	require.Equal(t, http.StatusAccepted, w.Code)

	// Without the Invalidate call the cached list would still report `before`.
	assert.Equal(t, before+1, list())
}

func TestDeleteTemplateHandler_InvalidatesCache(t *testing.T) {
	reg, r := setupTestRouter()
	tmplCache := newTemplateCacheForTest()
	a := reg.List()[0]
	build, err := a.CreateTemplate(context.Background(), &adapter.CreateTemplateRequest{Name: "cache-test-del"})
	require.NoError(t, err)

	// Warm cache; the warmed list must contain the template so the post-delete
	// assertion below cannot pass vacuously on an empty list.
	w := httptest.NewRecorder()
	v1.ListTemplatesHandlerWithCache(reg, r, tmplCache)(w, httptest.NewRequest(http.MethodGet, "/templates", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var warmed []dto.TemplateInfo
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &warmed))
	warmedIDs := make([]string, 0, len(warmed))
	for _, ti := range warmed {
		warmedIDs = append(warmedIDs, ti.TemplateID)
	}
	require.Contains(t, warmedIDs, build.TemplateID)

	// Delete through cache-aware handler.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("templateID", build.TemplateID)
	req := httptest.NewRequest(http.MethodDelete, "/templates/"+build.TemplateID, nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w = httptest.NewRecorder()
	v1.DeleteTemplateHandlerWithCache(reg, r, tmplCache)(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)

	// Next cached list must not contain the deleted template: without the
	// Invalidate call the warmed entry would still include it.
	w = httptest.NewRecorder()
	v1.ListTemplatesHandlerWithCache(reg, r, tmplCache)(w, httptest.NewRequest(http.MethodGet, "/templates", nil))
	var out []dto.TemplateInfo
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	for _, ti := range out {
		assert.NotEqual(t, build.TemplateID, ti.TemplateID)
	}
}

func TestReadyHandlerWithCache(t *testing.T) {
	reg, _ := setupTestRouter()
	hc := cache.New(10, cache.BackendHealthTTL)

	for range 2 {
		w := httptest.NewRecorder()
		v1.ReadyHandlerWithCache(reg, hc)(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		require.Equal(t, http.StatusOK, w.Code)
	}
	// Aggregated result is cached under "readyz".
	v, ok := hc.Get("readyz")
	require.True(t, ok)
	results, ok := v.(map[string]error)
	require.True(t, ok)
	assert.Contains(t, results, "mock")
}
