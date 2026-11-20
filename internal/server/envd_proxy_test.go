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

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/e2bgateway/e2bgateway/internal/envd"
)

func TestExtractSandboxIDFromHost(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		domain string
		want   string
	}{
		{
			name:   "port-sandboxID pattern",
			host:   "8080-abc123.example.com",
			domain: "example.com",
			want:   "abc123",
		},
		{
			name:   "sandboxID only pattern",
			host:   "abc123.example.com",
			domain: "example.com",
			want:   "abc123",
		},
		{
			name:   "sandbox prefix returns empty",
			host:   "sandbox.example.com",
			domain: "example.com",
			want:   "",
		},
		{
			name: "bare domain returns domain as prefix",
			// When host equals domain, TrimSuffix(".domain") doesn't match
			// so the full host is returned as the prefix (no dash → returned as-is).
			host:   "example.com",
			domain: "example.com",
			want:   "example.com",
		},
		{
			name:   "different domain returns empty",
			host:   "abc123.other.com",
			domain: "example.com",
			want:   "",
		},
		{
			name:   "host with port stripped",
			host:   "8080-abc123.example.com:443",
			domain: "example.com",
			want:   "abc123",
		},
		{
			name:   "empty domain returns empty",
			host:   "8080-abc123.example.com",
			domain: "",
			want:   "",
		},
		{
			name:   "multiple dashes in ID",
			host:   "8080-abc-123-def.example.com",
			domain: "example.com",
			want:   "abc-123-def",
		},
		{
			name: "nested subdomain extracts after first dash",
			// prefix = "foo.bar-abc123", first dash at index 7 → returns "abc123"
			host:   "foo.bar-abc123.example.com",
			domain: "example.com",
			want:   "abc123",
		},
		{
			name:   "empty host returns empty",
			host:   "",
			domain: "example.com",
			want:   "",
		},
		{
			name:   "port only sandboxID",
			host:   "49983-xyz789.example.com",
			domain: "example.com",
			want:   "xyz789",
		},
		{
			name:   "sandbox with port returns empty",
			host:   "sandbox.example.com:443",
			domain: "example.com",
			want:   "",
		},
		{
			name:   "no dash means entire prefix is sandbox ID",
			host:   "mySandbox123.example.com",
			domain: "example.com",
			want:   "mySandbox123",
		},
		{
			name:   "host not matching domain at all",
			host:   "completely-different.org",
			domain: "example.com",
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSandboxIDFromHost(tt.host, tt.domain)
			if got != tt.want {
				t.Errorf("extractSandboxIDFromHost(%q, %q) = %q, want %q", tt.host, tt.domain, got, tt.want)
			}
		})
	}
}

func TestSingleJoiningSlash(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want string
	}{
		{
			name: "both have slash",
			a:    "a/",
			b:    "/b",
			want: "a/b",
		},
		{
			name: "neither has slash",
			a:    "a",
			b:    "b",
			want: "a/b",
		},
		{
			name: "first has trailing slash only",
			a:    "a/",
			b:    "b",
			want: "a/b",
		},
		{
			name: "second has leading slash only",
			a:    "a",
			b:    "/b",
			want: "a/b",
		},
		{
			name: "empty first",
			a:    "",
			b:    "/b",
			want: "/b",
		},
		{
			name: "empty second",
			a:    "a",
			b:    "",
			want: "a/",
		},
		{
			name: "both slashes",
			a:    "/",
			b:    "/",
			want: "/",
		},
		{
			name: "both empty",
			a:    "",
			b:    "",
			want: "/",
		},
		{
			name: "real paths",
			a:    "/api/v1/proxy/49983",
			b:    "/process.Process/Start",
			want: "/api/v1/proxy/49983/process.Process/Start",
		},
		{
			name: "real paths with trailing slash on base",
			a:    "/api/v1/proxy/49983/",
			b:    "/process.Process/Start",
			want: "/api/v1/proxy/49983/process.Process/Start",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := singleJoiningSlash(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("singleJoiningSlash(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// TestNewEnvdReverseProxy_RewriteOnly guards the Go requirement that a
// ReverseProxy has exactly one of Director or Rewrite set. The previous
// NewSingleHostReverseProxy + Rewrite combination set BOTH, and every
// proxied data-plane request failed with 502 "ReverseProxy must have
// exactly one of Director or Rewrite set" (caught by the multi-replica E2E,
// the first test to reach proxy.ServeHTTP with a valid token).
func TestNewEnvdReverseProxy_RewriteOnly(t *testing.T) {
	target, err := url.Parse("http://envd.internal:49983/base")
	if err != nil {
		t.Fatal(err)
	}
	proxy := newEnvdReverseProxy(target, "tok-123")
	if proxy.Director != nil {
		t.Error("Director must be nil when Rewrite is used")
	}
	if proxy.Rewrite == nil {
		t.Error("Rewrite must be set")
	}
}

func TestNewEnvdReverseProxy_ForwardsRequest(t *testing.T) {
	var gotAuth, gotPath, gotHost, gotCT string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotHost = r.Host
		gotCT = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newEnvdReverseProxy(target, "tok-abc")

	req := httptest.NewRequest(http.MethodPost, "/process.Process/Start", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/connect+proto")
	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("proxy returned %d: %s", rr.Code, rr.Body.String())
	}
	if gotAuth != "Bearer tok-abc" {
		t.Errorf("Authorization header: got %q, want %q", gotAuth, "Bearer tok-abc")
	}
	if gotPath != "/process.Process/Start" {
		t.Errorf("backend path: got %q", gotPath)
	}
	if gotHost != target.Host {
		t.Errorf("backend Host: got %q, want %q", gotHost, target.Host)
	}
	if gotCT != "application/connect+proto" {
		t.Errorf("Content-Type: got %q", gotCT)
	}
}

func TestEnvdClientsAndProxyAfterInitSetsToken(t *testing.T) {
	const token = "test-token"
	storedToken := ""
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/init" {
			var body struct {
				AccessToken string `json:"accessToken"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.AccessToken != token {
				t.Error("initialization must set the body token")
			}
			storedToken = body.AccessToken
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if storedToken == "" || r.Header.Get("Authorization") != "Bearer "+storedToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"processes":[]}`))
	}))
	defer backend.Close()
	client := envd.NewClient(envd.ClientConfig{BaseURL: backend.URL, AccessToken: token})
	if err := client.InitEnvironment(context.Background(), map[string]string{"KEY": "value"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListProcesses(context.Background()); err != nil {
		t.Fatalf("internal envd client failed after token initialization: %v", err)
	}
	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/process.Process/List", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	newEnvdReverseProxy(target, token).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("ConnectRPC proxy failed after token initialization: HTTP %d", response.Code)
	}
}
