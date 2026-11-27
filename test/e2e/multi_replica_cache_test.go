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

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/e2bgateway/e2bgateway/internal/api/dto"
)

// TestMultiReplicaCacheInvalidation runs against TWO replicas sharing a
// Redis L2 (cache.tiered enabled on both). Proves issue #113 acceptance:
// a sandbox paused via replica A is observed as "paused" on replica B
// within 1s. Pause (not kill) is deliberate: killing removes the shared
// sandbox→backend routing entry, so replica B would 404 at adapter
// resolution even without cache invalidation — a vacuous check. With
// pause the routing entry survives, so replica B can only report the new
// state if the invalidation broadcast evicted its stale cached
// "running" entry (TTL 30s otherwise).
func TestMultiReplicaCacheInvalidation(t *testing.T) {
	urlA := os.Getenv("GATEWAY_URL")
	urlB := os.Getenv("GATEWAY_URL_B")
	if urlA == "" || urlB == "" {
		t.Skip("GATEWAY_URL / GATEWAY_URL_B not set — multi-replica E2E only")
	}
	apiKey := os.Getenv("E2B_API_KEY")
	client := &http.Client{Timeout: 30 * time.Second}

	// do never calls require: it is also used inside the Eventually
	// condition, which runs on a separate goroutine where testify
	// FailNow is not allowed. Callers on the test goroutine assert.
	do := func(method, url, body string) (int, []byte, error) {
		var rdr io.Reader
		if body != "" {
			rdr = bytes.NewReader([]byte(body))
		}
		req, err := http.NewRequest(method, url, rdr)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", apiKey)
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return resp.StatusCode, b, err
	}

	// 1. Create via A.
	code, body, err := do(http.MethodPost, urlA+"/sandboxes", `{"templateID":"base","timeout":300}`)
	require.NoError(t, err)
	require.Contains(t, []int{http.StatusOK, http.StatusCreated}, code, "create: %s", body)
	var created dto.SandboxCreateResponse
	require.NoError(t, json.Unmarshal(body, &created))
	require.NotEmpty(t, created.SandboxID, "create response missing sandboxID: %s", body)
	t.Cleanup(func() {
		_, _, _ = do(http.MethodDelete, urlA+"/sandboxes/"+created.SandboxID, "")
	})

	// 2. Warm B's cache: GET must succeed and report "running".
	code, body, err = do(http.MethodGet, urlB+"/sandboxes/"+created.SandboxID, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code, "warm B: %s", body)
	var info dto.SandboxInfo
	require.NoError(t, json.Unmarshal(body, &info))
	require.Equal(t, "running", info.State, "warm B state: %s", body)

	// 3. Pause via A (routing entry survives; only state changes).
	code, body, err = do(http.MethodPost, urlA+"/sandboxes/"+created.SandboxID+"/pause", "")
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, code, "pause A: %s", body)

	// 4. B must report "paused" within 1s (invalidation broadcast).
	// Without the broadcast B serves the stale cached "running" entry
	// for the full 30s TTL. No require.* in here: condition goroutine.
	require.Eventually(t, func() bool {
		code, body, err := do(http.MethodGet, urlB+"/sandboxes/"+created.SandboxID, "")
		if err != nil || code != http.StatusOK {
			return false
		}
		var info dto.SandboxInfo
		if err := json.Unmarshal(body, &info); err != nil {
			return false
		}
		return info.State == "paused"
	}, time.Second, 20*time.Millisecond, "replica B still serves stale cached state after pause via A")

	// 5. Cache metrics exist on B (hit-rate evidence for QPS reduction).
	code, body, err = do(http.MethodGet, urlB+"/metrics", "")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	for _, name := range []string{
		"e2bgw_cache_l1_hits_total",
		"e2bgw_cache_misses_total",
		"e2bgw_cache_invalidations_total",
	} {
		require.Contains(t, string(body), name)
	}
}
