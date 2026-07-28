// Package server — envd_proxy.go implements a reverse-proxy handler that
// forwards E2B SDK ConnectRPC requests to the correct sandbox's envd daemon.
//
// The E2B Python/JS SDK constructs envd URLs as:
//
//	https://{port}-{sandboxID}.{sandboxDomain}
//
// and sends these headers on every request:
//
//	E2b-Sandbox-Id:   the sandbox ID
//	E2b-Sandbox-Port: the target port inside the sandbox (usually 49983)
//	X-Access-Token:   the envd access token
//
// This handler extracts the sandbox ID (from the header or the Host header),
// asks the adapter for the envd endpoint, and reverse-proxies the request.
// httputil.ReverseProxy natively supports HTTP streaming, which is required
// for ConnectRPC server-stream RPCs (process.Process/Start, etc.).
package server

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/e2bgateway/e2bgateway/internal/routing"
)

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

		// Select the backend adapter for this sandbox.
		backendName, err := s.routeMgr.SelectBackend(r.Context(), &routing.RoutingRequest{})
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"code":503,"message":"%s"}`, err.Error()), http.StatusServiceUnavailable)
			return
		}

		a, ok := s.registry.Get(backendName)
		if !ok {
			http.Error(w, `{"code":503,"message":"backend not found"}`, http.StatusServiceUnavailable)
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

		// Preserve the original Host header so envd's CORS / routing works.
		proxy.Director = func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host

			// Forward the access token as Authorization: Bearer.
			if token := r.Header.Get("X-Access-Token"); token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
		}

		// ErrorHandler returns a JSON error instead of plain text.
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, fmt.Sprintf(`{"code":502,"message":"envd proxy error: %s"}`, err.Error()), http.StatusBadGateway)
		}

		proxy.ServeHTTP(w, r)
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
	if idx := strings.Index(prefix, "-"); idx >= 0 {
		return prefix[idx+1:]
	}

	// Pattern: {sandboxID}
	return prefix
}
