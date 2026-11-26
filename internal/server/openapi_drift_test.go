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
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.yaml.in/yaml/v3"

	"github.com/e2bgateway/e2bgateway/internal/config"
)

// openAPIPath is the API contract kept in sync with the chi route table by
// TestOpenAPISpecMatchesRoutes.
const openAPIPath = "../../docs/api/openapi.yaml"

// httpMethods are the OpenAPI path-item keys that define operations. Any
// other key under a path item (e.g. "parameters") is not an operation.
var httpMethods = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true,
	"delete": true, "head": true, "options": true, "trace": true,
}

// exemptRoutes lists registered routes intentionally absent from
// openapi.yaml. Keys are "METHOD /path"; method "ALL" matches any method on
// that path. Every entry must match at least one registered route — stale
// exemptions fail the test so the list cannot silently rot. Add new entries
// only for internal, infrastructure, or deprecated-legacy endpoints, always
// with a reason.
var exemptRoutes = map[string]string{
	"ALL /*":       "envd data-plane catch-all reverse proxy (ConnectRPC/WebSocket passthrough), not a REST endpoint",
	"GET /metrics": "Prometheus scrape endpoint (infrastructure, not part of the API contract)",

	// Legacy envd-compatible aliases of documented filesystem/commands routes.
	"POST /sandboxes/{sandboxID}/files":                       "legacy alias of POST /sandboxes/{sandboxID}/filesystem/upload",
	"GET /sandboxes/{sandboxID}/files":                        "legacy alias of GET /sandboxes/{sandboxID}/filesystem/download",
	"POST /sandboxes/{sandboxID}/files/upload":                "legacy alias of POST /sandboxes/{sandboxID}/filesystem/upload",
	"GET /sandboxes/{sandboxID}/files/download":               "legacy alias of GET /sandboxes/{sandboxID}/filesystem/download",
	"POST /sandboxes/{sandboxID}/files/list":                  "legacy alias of POST /sandboxes/{sandboxID}/filesystem/list",
	"POST /sandboxes/{sandboxID}/files/make-dir":              "legacy alias of POST /sandboxes/{sandboxID}/filesystem/mkdir",
	"POST /sandboxes/{sandboxID}/files/remove":                "legacy alias of POST /sandboxes/{sandboxID}/filesystem/rm",
	"GET /sandboxes/{sandboxID}/processes":                    "legacy alias of GET /sandboxes/{sandboxID}/commands",
	"POST /sandboxes/{sandboxID}/processes/{processID}/kill":  "legacy alias of POST /sandboxes/{sandboxID}/commands/{processID}/kill",
	"POST /sandboxes/{sandboxID}/processes/{processID}/stdin": "legacy alias of POST /sandboxes/{sandboxID}/commands/{processID}/input",
}

// exemptPrefixes exempts whole route subtrees. Every prefix must match at
// least one registered route.
var exemptPrefixes = map[string]string{
	"/api/v1/": "deprecated legacy prefix mirroring the root-level API; its async-execution handlers are stubs pending #106",
}

// newDriftTestServer builds a Server whose router carries the full route
// table. Metrics are enabled so the conditional /metrics route is registered
// (and exempted) rather than silently absent from the inventory.
func newDriftTestServer(t *testing.T) *Server {
	t.Helper()
	srv, err := New(&config.Config{
		Server: config.ServerConfig{
			HTTP: config.HTTPConfig{Address: "127.0.0.1:0"},
		},
		Backends: []config.BackendConfig{{Name: "mock", Type: "mock", Enabled: true}},
		Routing:  config.RoutingConfig{DefaultBackend: "mock", Strategy: "static"},
		Observability: config.ObservabilityConfig{
			Logging: config.LoggingConfig{Level: "debug"},
			Metrics: config.MetricsConfig{Enabled: true, Path: "/metrics", Prefix: "e2bgw"},
		},
	})
	if err != nil {
		t.Fatalf("creating server: %v", err)
	}
	return srv
}

// registeredRoutes walks the chi router and returns the set of
// "METHOD /path" routes actually registered. chi reports method "*" for
// catch-all handlers; it is normalized to "ALL".
func registeredRoutes(t *testing.T, r chi.Router) map[string]bool {
	t.Helper()
	routes := map[string]bool{}
	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		m := strings.ToUpper(method)
		if m == "*" {
			m = "ALL"
		}
		routes[m+" "+route] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walking router: %v", err)
	}
	return routes
}

// specRoutes parses openapi.yaml and returns the set of "METHOD /path"
// operations it documents.
func specRoutes(t *testing.T) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(openAPIPath)
	if err != nil {
		t.Fatalf("reading %s: %v", openAPIPath, err)
	}
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing %s: %v", openAPIPath, err)
	}
	routes := map[string]bool{}
	for path, item := range doc.Paths {
		for key := range item {
			if httpMethods[strings.ToLower(key)] {
				routes[strings.ToUpper(key)+" "+path] = true
			}
		}
	}
	return routes
}

// exemptRule returns the exemption rule covering the given "METHOD /path"
// route, or "" when the route is not exempt.
func exemptRule(route string) string {
	if _, ok := exemptRoutes[route]; ok {
		return route
	}
	_, path, found := strings.Cut(route, " ")
	if !found {
		return ""
	}
	if _, ok := exemptRoutes["ALL "+path]; ok {
		return "ALL " + path
	}
	for prefix := range exemptPrefixes {
		if strings.HasPrefix(path, prefix) {
			return "prefix:" + prefix
		}
	}
	return ""
}

// TestOpenAPISpecMatchesRoutes fails when the chi route table and
// docs/api/openapi.yaml diverge: a registered route missing from the spec
// (unless exempt), or a spec operation with no registered route (ghost).
func TestOpenAPISpecMatchesRoutes(t *testing.T) {
	srv := newDriftTestServer(t)
	actual := registeredRoutes(t, srv.router)
	spec := specRoutes(t)

	usedExemptions := map[string]bool{}
	var missingFromSpec, ghostsInSpec []string

	for route := range actual {
		if spec[route] {
			continue
		}
		if rule := exemptRule(route); rule != "" {
			usedExemptions[rule] = true
			continue
		}
		missingFromSpec = append(missingFromSpec, route)
	}
	for route := range spec {
		if !actual[route] {
			ghostsInSpec = append(ghostsInSpec, route)
		}
	}

	var staleExemptions []string
	for key := range exemptRoutes {
		if !usedExemptions[key] {
			staleExemptions = append(staleExemptions, key)
		}
	}
	for prefix := range exemptPrefixes {
		if !usedExemptions["prefix:"+prefix] {
			staleExemptions = append(staleExemptions, "prefix:"+prefix)
		}
	}

	sort.Strings(missingFromSpec)
	sort.Strings(ghostsInSpec)
	sort.Strings(staleExemptions)

	if len(missingFromSpec) > 0 {
		t.Errorf("routes registered but missing from %s (add them to the spec, or to exemptRoutes with a reason):\n  %s",
			openAPIPath, strings.Join(missingFromSpec, "\n  "))
	}
	if len(ghostsInSpec) > 0 {
		t.Errorf("operations documented in %s but not registered in the router (remove them or fix the path/method):\n  %s",
			openAPIPath, strings.Join(ghostsInSpec, "\n  "))
	}
	if len(staleExemptions) > 0 {
		t.Errorf("exemptions that match no registered route (remove them):\n  %s",
			strings.Join(staleExemptions, "\n  "))
	}
}
