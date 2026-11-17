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
	"strings"
	"testing"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/api/dto"
)

// TestMultiReplicaDataPlane runs against TWO port-forwards pinned to two
// different gateway replicas (GATEWAY_URL → pod A, GATEWAY_URL_B → pod B),
// both backed by a shared redis sessionStore. It proves issue #90's core
// acceptance: a sandbox created via replica A serves data-plane requests on
// replica B with no 401.
func TestMultiReplicaDataPlane(t *testing.T) {
	urlA := os.Getenv("GATEWAY_URL")
	urlB := os.Getenv("GATEWAY_URL_B")
	if urlA == "" || urlB == "" {
		t.Skip("GATEWAY_URL / GATEWAY_URL_B not set — multi-replica E2E only")
	}
	apiKey := os.Getenv("E2B_API_KEY")
	client := &http.Client{Timeout: 120 * time.Second}

	// 1. Create a sandbox pinned to replica A.
	createBody, _ := json.Marshal(dto.SandboxCreateRequest{TemplateID: "base"})
	req, _ := http.NewRequest(http.MethodPost, urlA+"/sandboxes", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("create on replica A: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("create on replica A: status %d body %s", resp.StatusCode, body)
	}
	var created dto.SandboxCreateResponse
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decoding create response: %v (%s)", err, body)
	}
	if created.SandboxID == "" || created.EnvdAccessToken == "" {
		t.Fatalf("create response missing sandboxID/envdAccessToken: %s", body)
	}
	t.Cleanup(func() {
		delReq, _ := http.NewRequest(http.MethodDelete, urlA+"/sandboxes/"+created.SandboxID, nil)
		delReq.Header.Set("X-API-Key", apiKey)
		if delResp, err := client.Do(delReq); err == nil {
			_ = delResp.Body.Close()
		}
	})

	// 2. Data-plane request pinned to replica B with the token minted by A.
	// Any unmatched path routes into the envd proxy. Failure modes that must
	// NOT appear: 401 (replica B could not validate replica A's token — the
	// issue #90 regression), 502 (session-store outage — proves nothing), and
	// the gateway's own 404 body "sandbox not found" (routing entry missing
	// on replica B). Any other response (e.g. envd's own status for the probe
	// path) proves the token validated and the request was routed off-box.
	dpReq, _ := http.NewRequest(http.MethodGet, urlB+"/multi-replica-probe", nil)
	dpReq.Header.Set("E2b-Sandbox-Id", created.SandboxID)
	dpReq.Header.Set("X-Access-Token", created.EnvdAccessToken)
	dpResp, err := client.Do(dpReq)
	if err != nil {
		t.Fatalf("data-plane request on replica B: %v", err)
	}
	defer dpResp.Body.Close()
	dpBody, _ := io.ReadAll(dpResp.Body)
	switch dpResp.StatusCode {
	case http.StatusUnauthorized:
		t.Fatalf("replica B rejected replica A's token with 401 (issue #90 not fixed): %s", dpBody)
	case http.StatusBadGateway:
		t.Fatalf("replica B could not reach the session store (502) — cross-replica validation unproven: %s", dpBody)
	}
	if strings.Contains(string(dpBody), "sandbox not found") {
		t.Fatalf("replica B has no routing entry for replica A's sandbox (gateway 404): %s", dpBody)
	}

	// 3. A wrong token must still be rejected on replica B (validation is real).
	badReq, _ := http.NewRequest(http.MethodGet, urlB+"/multi-replica-probe", nil)
	badReq.Header.Set("E2b-Sandbox-Id", created.SandboxID)
	badReq.Header.Set("X-Access-Token", "envd_wrong_token")
	badResp, err := client.Do(badReq)
	if err != nil {
		t.Fatalf("bad-token request: %v", err)
	}
	defer badResp.Body.Close()
	if badResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong token on replica B, got %d", badResp.StatusCode)
	}
}
