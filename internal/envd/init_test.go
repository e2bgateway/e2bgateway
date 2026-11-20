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

package envd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInitEnvironmentAccessToken(t *testing.T) {
	for _, initialToken := range []string{"", "test-token"} {
		name := "first_init"
		if initialToken != "" {
			name = "existing_token"
		}
		t.Run(name, func(t *testing.T) {
			storedToken := initialToken
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/init" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				var body struct {
					EnvVars     map[string]string `json:"envVars"`
					AccessToken string            `json:"accessToken"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if body.EnvVars["CREATION_ENV"] != "present" {
					t.Error("missing creation environment")
				}
				if r.Header.Get("X-Access-Token") != body.AccessToken {
					t.Error("header and body tokens must match")
				}
				// envd 2026.16 validates the body token, even when the
				// authentication header already contains the expected value.
				if storedToken != "" && body.AccessToken != storedToken {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				storedToken = body.AccessToken
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			client := NewClient(ClientConfig{BaseURL: server.URL, AccessToken: "test-token"})
			envs := map[string]string{"CREATION_ENV": "present"}
			for range 2 {
				if err := client.InitEnvironment(context.Background(), envs); err != nil {
					t.Fatalf("initialization with the same token must succeed: %v", err)
				}
			}
			for _, token := range []string{"wrong-token", ""} {
				client := NewClient(ClientConfig{BaseURL: server.URL, AccessToken: token})
				if err := client.InitEnvironment(context.Background(), envs); err == nil {
					t.Error("a different or missing token must be rejected")
				}
			}
		})
	}
}

func TestInitEnvironmentOmitsEmptyAccessToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if _, ok := body["accessToken"]; ok {
			t.Error("an empty access token must be omitted")
		}
		if r.Header.Get("X-Access-Token") != "" {
			t.Error("an empty access token must not be sent in the header")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := NewClient(ClientConfig{BaseURL: server.URL})
	if err := client.InitEnvironment(context.Background(), map[string]string{"KEY": "value"}); err != nil {
		t.Fatal(err)
	}
}
