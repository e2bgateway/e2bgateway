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

package client

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadFile(t *testing.T) {
	const path = "/tmp/hello & world.txt"
	server := testServer(t, map[string]http.HandlerFunc{
		"GET /sandboxes/sb-1/files": func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "test-key", r.Header.Get("X-API-Key"))
			assert.Equal(t, path, r.URL.Query().Get("path"))
			writeJSON(t, w, http.StatusOK, "file contents")
		},
	})
	defer server.Close()

	c := New(server.URL, WithAPIKey("test-key"))
	content, err := c.ReadFile(t.Context(), "sb-1", path)
	require.NoError(t, err)
	assert.Equal(t, "file contents", content)
}

func TestExecuteCode_APIError(t *testing.T) {
	server := testServer(t, map[string]http.HandlerFunc{
		"POST /sandboxes/sb-1/code": func(w http.ResponseWriter, _ *http.Request) {
			writeErr(t, w, http.StatusNotFound, "sandbox not found")
		},
	})
	defer server.Close()

	_, err := New(server.URL).ExecuteCode(t.Context(), "sb-1", CodeExecRequest{Code: "print(1)"})
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.StatusCode)
	assert.Equal(t, "sandbox not found", apiErr.Message)
}
