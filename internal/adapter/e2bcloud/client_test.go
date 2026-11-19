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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/e2bgateway/e2bgateway/internal/api/dto"
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

func TestStartTemplateBuild_Redirects(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var hits int
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/target" {
					http.Redirect(w, r, "/target", status)
					return
				}
				hits++
				if r.Method != http.MethodPost {
					t.Errorf("method = %s", r.Method)
				}
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), "base") {
					t.Errorf("missing payload: %s", body)
				}
				w.WriteHeader(http.StatusAccepted)
			}))
			defer ts.Close()
			c := NewClient(ClientConfig{Endpoint: ts.URL})
			c.maxRetries = 0
			err := c.StartTemplateBuild(context.Background(), "template", "build", &dto.TemplateBuildStartV2{FromImage: "base"})
			if status == 307 || status == 308 {
				if err != nil || hits != 1 {
					t.Fatalf("err=%v hits=%d", err, hits)
				}
			} else if err == nil || hits != 0 {
				t.Fatalf("err=%v hits=%d", err, hits)
			}
		})
	}
}
func TestStartTemplateBuild_RedirectLimit(t *testing.T) {
	hits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Redirect(w, r, "/loop", http.StatusTemporaryRedirect)
	}))
	defer ts.Close()
	c := NewClient(ClientConfig{Endpoint: ts.URL})
	c.maxRetries = 0
	err := c.StartTemplateBuild(context.Background(), "template", "build", &dto.TemplateBuildStartV2{})
	if err == nil || hits != 10 {
		t.Fatalf("err=%v hits=%d", err, hits)
	}
}
