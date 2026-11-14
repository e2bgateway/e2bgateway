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

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

type WarmPoolCreateRequest struct {
	TemplateID string `json:"templateID"`
	TargetSize int    `json:"targetSize"`
	WarmPoolID string `json:"warmPoolID,omitempty"`
}

// uniquePoolID builds an RFC 1123-compatible, run-unique pool ID. Force
// delete tombstones the namespace briefly; relying on the deterministic
// auto-derived ID (wp-base-…) would make an immediate re-run collide with
// the tombstone.
func uniquePoolID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixMilli())
}

type WarmPoolInfo struct {
	WarmPoolID  string `json:"warmPoolID"`
	TemplateID  string `json:"templateID"`
	TargetSize  int    `json:"targetSize"`
	CurrentSize int    `json:"currentSize"`
	Status      string `json:"status"`
}

var (
	baseURL = envOr("E2B_DOMAIN", "localhost:8080")
	apiKey  = envOr("E2B_API_KEY", "test-key")
	client  = &http.Client{Timeout: 120 * time.Second}
	created []string // pools to best-effort clean up on failure
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func do(method, path string, body any, query map[string]string) (int, []byte, error) {
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, baseURL+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	q := req.URL.Query()
	for k, v := range query {
		q.Set(k, v)
	}
	req.URL.RawQuery = q.Encode()
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes(), nil
}

func must(status int, body []byte, err error, step string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", step, err)
		cleanup()
		os.Exit(1)
	}
	if status >= 400 {
		fmt.Fprintf(os.Stderr, "%s: HTTP %d: %s\n", step, status, body)
		cleanup()
		os.Exit(1)
	}
}

func cleanup() {
	for _, id := range created {
		_, _, _ = do(http.MethodDelete, "/warm-pools/"+id, nil, map[string]string{"force": "true"})
	}
}

func main() {
	if !startsWithHTTP(baseURL) {
		baseURL = "http://" + baseURL
	}

	fmt.Println("1. Listing warm pools...")
	status, body, err := do(http.MethodGet, "/warm-pools", nil, nil)
	must(status, body, err, "list")
	var pools []WarmPoolInfo
	_ = json.Unmarshal(body, &pools)
	fmt.Printf("   Existing pools: %d\n", len(pools))

	fmt.Println("2. Creating warm pool...")
	status, body, err = do(http.MethodPost, "/warm-pools",
		WarmPoolCreateRequest{TemplateID: "base", TargetSize: 3, WarmPoolID: uniquePoolID("wp-go")}, nil)
	must(status, body, err, "create")
	var pool WarmPoolInfo
	_ = json.Unmarshal(body, &pool)
	created = append(created, pool.WarmPoolID)
	fmt.Printf("   Pool: %s (template=%s, target=%d)\n", pool.WarmPoolID, pool.TemplateID, pool.TargetSize)

	fmt.Println("3. Getting warm pool details...")
	status, body, err = do(http.MethodGet, "/warm-pools/"+pool.WarmPoolID, nil, nil)
	must(status, body, err, "get")
	var detail WarmPoolInfo
	_ = json.Unmarshal(body, &detail)
	fmt.Printf("   Status: %s, target=%d, current=%d\n", detail.Status, detail.TargetSize, detail.CurrentSize)

	fmt.Println("4. Updating pool size...")
	status, body, err = do(http.MethodPost, "/warm-pools/"+pool.WarmPoolID+"/size",
		map[string]int{"targetSize": 5}, nil)
	must(status, body, err, "resize")
	fmt.Printf("   Updated: HTTP %d\n", status)

	fmt.Println("5. Deleting warm pool (graceful)...")
	status, body, err = do(http.MethodDelete, "/warm-pools/"+pool.WarmPoolID, nil, nil)
	must(status, body, err, "delete")
	created = created[:0]
	fmt.Printf("   Deleted: HTTP %d\n", status)

	fmt.Println("6. Force-deleting a warm pool...")
	status, body, err = do(http.MethodPost, "/warm-pools",
		WarmPoolCreateRequest{TemplateID: "base", TargetSize: 1, WarmPoolID: uniquePoolID("wp-go-force")}, nil)
	must(status, body, err, "create-for-force")
	var forcePool WarmPoolInfo
	_ = json.Unmarshal(body, &forcePool)
	created = append(created, forcePool.WarmPoolID)
	status, body, err = do(http.MethodDelete, "/warm-pools/"+forcePool.WarmPoolID, nil,
		map[string]string{"force": "true"})
	must(status, body, err, "force-delete")
	created = created[:0]
	fmt.Printf("   Force deleted %s: HTTP %d\n", forcePool.WarmPoolID, status)

	fmt.Println("\nDone.")
}

func startsWithHTTP(s string) bool {
	return len(s) >= 7 && (s[:7] == "http://" || (len(s) >= 8 && s[:8] == "https://"))
}
