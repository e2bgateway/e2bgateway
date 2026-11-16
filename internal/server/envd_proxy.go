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
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/metrics"
	"github.com/e2bgateway/e2bgateway/internal/routing"
)

// proxyStatusWriter captures the upstream response status for metrics.
// The middleware's own statusWriter is unexported and not reachable here.
type proxyStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *proxyStatusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *proxyStatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// envdProxyTransport is a shared http.Transport for all envd reverse proxies.
// Creating a new Transport per request would allocate a new connection pool
// each time, preventing TCP connection reuse across SDK calls.
var envdProxyTransport = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	MaxIdleConns:          100,
	MaxIdleConnsPerHost:   100,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}

// envdProxyHandler returns an http.Handler that reverse-proxies ConnectRPC
// requests to the envd daemon inside the target sandbox container.
func (s *Server) envdProxyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sandboxID := r.Header.Get("E2b-Sandbox-Id")
		if sandboxID == "" {
			sandboxID = extractSandboxIDFromHost(r.Host, s.cfg.Server.EnvdDomain)
		}
		if sandboxID == "" {
			http.Error(w, `{"code":400,"message":"missing sandbox ID"}`, http.StatusBadRequest)
			return
		}

		// FIX: Use sandbox→backend mapping for correct backend selection.
		// This fixes the bug where empty RoutingRequest could route to wrong backend.
		var a adapter.SandboxAdapter
		var ok bool
		// backendName is hoisted so both selection branches feed the same
		// variable used for metric labels below.
		backendName := ""

		if s.registry.SandboxBackend() != nil {
			name, found, err := s.registry.SandboxBackend().Get(sandboxID)
			if err != nil {
				slog.Error("session store error during sandbox routing lookup", "op", "routing_get", "sandboxID", sandboxID, "error", err)
				http.Error(w, `{"code":502,"message":"sandbox routing lookup failed"}`, http.StatusBadGateway)
				return
			}
			if found {
				backendName = name
				a, ok = s.registry.Get(name)
			}
		}

		// Fallback: use routing manager if mapping doesn't have the sandbox
		if !ok {
			name, err := s.routeMgr.SelectBackend(r.Context(), &routing.RoutingRequest{})
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"code":503,"message":"%s"}`, err.Error()), http.StatusServiceUnavailable)
				return
			}
			backendName = name
			a, ok = s.registry.Get(name)
			if !ok {
				http.Error(w, `{"code":503,"message":"backend not found"}`, http.StatusServiceUnavailable)
				return
			}
		}

		// Validate access token before proxying.
		token := r.Header.Get("X-Access-Token")
		if token == "" {
			metrics.RecordTokenFailure(backendName, "missing")
			http.Error(w, `{"code":401,"message":"missing access token"}`, http.StatusUnauthorized)
			return
		}
		valid, err := a.ValidateAccessToken(r.Context(), sandboxID, token)
		if err != nil || !valid {
			metrics.RecordTokenFailure(backendName, "invalid")
			http.Error(w, `{"code":401,"message":"invalid access token"}`, http.StatusUnauthorized)
			return
		}

		envdURL, _, err := a.GetEnvdEndpoint(r.Context(), sandboxID)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"code":502,"message":"%s"}`, err.Error()), http.StatusBadGateway)
			return
		}

		target, err := url.Parse(envdURL)
		if err != nil {
			http.Error(w, `{"code":500,"message":"invalid envd endpoint"}`, http.StatusInternalServerError)
			return
		}

		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.Transport = envdProxyTransport

		// Rewrite adapts the incoming request for the upstream envd service.
		proxy.Rewrite = func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// Preserve the original path-joining behavior.
			pr.Out.URL.Path = singleJoiningSlash(target.Path, pr.In.URL.Path)
			// Preserve the original Host header so envd's CORS / routing works.
			pr.Out.Host = target.Host
			// Forward the validated access token as Authorization: Bearer.
			pr.Out.Header.Set("Authorization", "Bearer "+token)
			// Preserve original Content-Type for ConnectRPC protocol detection.
			if ct := pr.In.Header.Get("Content-Type"); ct != "" {
				pr.Out.Header.Set("Content-Type", ct)
			}
		}

		// ErrorHandler returns a JSON error instead of plain text.
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, fmt.Sprintf(`{"code":502,"message":"envd proxy error: %s"}`, err.Error()), http.StatusBadGateway)
		}

		// proxy.ErrorHandler writes its 502 through ww as well, so the wrapper
		// captures every response without double-recording.
		proxyStart := time.Now()
		ww := &proxyStatusWriter{ResponseWriter: w, status: http.StatusOK}
		proxy.ServeHTTP(ww, r)
		metrics.RecordEnvdProxy(backendName, ww.status, time.Since(proxyStart))
	})
}

// extractSandboxIDFromHost parses the sandbox ID from a Host header.
//
// E2B SDK URL patterns:
//
//	{port}-{sandboxID}.{domain}       (non-supported domains)
//	sandbox.{domain}                   (supported domains — ID is in header)
//	{sandboxID}.{domain}              (some SDK versions)
//
// Returns "" if the ID cannot be extracted.
func extractSandboxIDFromHost(host string, domain string) string {
	// Strip port if present.
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		host = host[:idx]
	}

	if domain == "" {
		return ""
	}

	// Remove the domain suffix.
	if !strings.HasSuffix(host, "."+domain) && host != domain {
		return ""
	}
	prefix := strings.TrimSuffix(host, "."+domain)
	if prefix == "" || prefix == "sandbox" {
		return ""
	}

	// Pattern: {port}-{sandboxID}
	if _, after, ok := strings.Cut(prefix, "-"); ok {
		return after
	}

	// Pattern: {sandboxID}
	return prefix
}

// singleJoiningSlash joins two path segments with a single slash, avoiding double slashes.
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}
