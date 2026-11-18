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

package e2bcloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestClient_RejectsCrossOriginRedirect(t *testing.T) {
	var targetHits atomic.Int32

	// A different "origin" (different port) that must never receive the API key.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/templates", http.StatusFound)
	}))
	defer source.Close()

	c := NewClient(ClientConfig{Endpoint: source.URL, APIKey: "secret-key"})
	_, err := c.ListTemplates(context.Background())
	if err == nil {
		t.Fatal("expected an error when the API redirects to a different origin")
	}
	if targetHits.Load() != 0 {
		t.Error("client must not follow a cross-origin redirect (X-API-Key would leak)")
	}
}

func TestClient_AllowsSameOriginRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/templates", func(w http.ResponseWriter, r *http.Request) {
		// Same origin, different path — must be followed.
		http.Redirect(w, r, "/v2/templates", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/v2/templates", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "secret-key"})
	if _, err := c.ListTemplates(context.Background()); err != nil {
		t.Fatalf("same-origin redirect should be followed, got error: %v", err)
	}
}
