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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionHandler(t *testing.T) {
	tests := []struct {
		name string
		info BuildInfo
		want map[string]string
	}{
		{
			name: "explicit metadata",
			info: BuildInfo{Version: "v1.2.3", Commit: "abc1234", BuildDate: "2026-11-26T00:00:00Z"},
			want: map[string]string{
				"version":   "v1.2.3",
				"commit":    "abc1234",
				"buildDate": "2026-11-26T00:00:00Z",
				"goVersion": runtime.Version(),
			},
		},
		{
			name: "defaults for empty metadata",
			info: BuildInfo{},
			want: map[string]string{
				"version":   "dev",
				"commit":    "unknown",
				"buildDate": "unknown",
				"goVersion": runtime.Version(),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			VersionHandler(tt.info)(rec, httptest.NewRequest(http.MethodGet, "/versionz", nil))

			assert.Equal(t, http.StatusOK, rec.Code)
			var body map[string]string
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			for field, want := range tt.want {
				assert.Equal(t, want, body[field], "field %q", field)
			}
		})
	}
}
