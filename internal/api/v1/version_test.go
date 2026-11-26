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

package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

func TestVersionHandler(t *testing.T) {
	h := VersionHandler(BuildInfo{Version: "v1.2.3", Commit: "abc1234", BuildDate: "2026-11-26T00:00:00Z"})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/versionz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]string{
		"version":   "v1.2.3",
		"commit":    "abc1234",
		"buildDate": "2026-11-26T00:00:00Z",
		"goVersion": runtime.Version(),
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%q] = %q, want %q", k, body[k], v)
		}
	}
}

func TestVersionHandlerDefaults(t *testing.T) {
	h := VersionHandler(BuildInfo{})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/versionz", nil))

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["version"] != "dev" || body["commit"] != "unknown" || body["buildDate"] != "unknown" {
		t.Errorf("expected dev/unknown defaults, got %v", body)
	}
}