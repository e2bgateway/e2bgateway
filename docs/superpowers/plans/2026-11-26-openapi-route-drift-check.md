# OpenAPI Route Drift Check (issue #118) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make CI catch any drift between the chi route table (`internal/server/http.go`) and the API contract (`docs/api/openapi.yaml`), close the current gaps, and enforce error-response schema quality.

**Architecture:** A Go test in `internal/server` (package `server`, so it can read the unexported `srv.router`) walks the chi router with `chi.Walk`, parses `docs/api/openapi.yaml`, and diffs `METHOD /path` sets in both directions — routes missing from the spec and ghost spec entries both fail. An exemption list (exact `METHOD /path` keys plus path prefixes, each with a reason string) covers internal/legacy endpoints; stale exemptions also fail. A second test gates that every non-probe operation documents an error response referencing the shared `Error`/`AuthError` schemas. `make openapi-check` runs both; the `check` workflow calls it on every PR.

**Tech Stack:** Go 1.26, chi/v5 (`chi.Walk`), `go.yaml.in/yaml/v3` (already an indirect dep; will become direct), GitHub Actions (existing `.github/workflows/check.yml`).

## Global Constraints

- Module: `github.com/e2bgateway/e2bgateway`; branch `feat/118-openapi-route-drift-check` (already created from `origin/main`).
- Every commit uses `git commit -s` (DCO sign-off — user preference).
- Conventional Commits (`test(api):`, `docs(api):`, `ci:` …) per `.agents/skills/submit-pr/SKILL.md`.
- New Go files start with the same Apache license header as existing files ("Copyright The E2BGateway Authors").
- OpenAPI dialect is **3.0.3** (no `$ref`-able path items — duplicate entries instead of referencing).
- Copy style: plain, no AI-flavored filler (user preference).
- Route inventory ground truth (verified against `internal/server/http.go` @ origin/main):
  - Spec gaps to fill: `GET /sandboxes/{sandboxID}/ws`, and `/v2` filesystem ×6 (`POST upload`, `GET download`, `POST list`, `POST mkdir`, `POST rm`, `POST move`).
  - Exempt: `/*` (envd proxy catch-all), `GET /metrics`, legacy root-level `/sandboxes/{sandboxID}/files*` (7 routes) and `/sandboxes/{sandboxID}/processes*` (3 routes), and the whole `/api/v1/` subtree (deprecated mirror; its async-executions handlers are stubs — `GetExecutionHandler` returns 501, issue #106 not landed).
  - v2 snapshot routes / tenants (#104/#106) do NOT exist in the router — nothing to document. warm-pools `force` param and `/v2` logs/metrics/templates/tags are already in the spec.
  - Error-gate violations to fix (18 operations): `POST/GET snapshots`, `GET ports`, `POST access-token`, `GET logs`, `GET+POST /templates`, `POST aliases`, `POST+GET tags`, `GET+POST /warm-pools`, `GET /v2/sandboxes`, `GET /v2/.../logs`, `GET+POST /v2/templates`, `POST+GET /v2/.../tags` (GET /healthz is exempt as a probe).

---

### Task 1: Route inventory drift test (red)

**Files:**
- Create: `internal/server/openapi_drift_test.go`
- Modify: `go.mod`, `go.sum` (via `go mod tidy` — promotes `go.yaml.in/yaml/v3` to a direct dependency)

**Interfaces:**
- Consumes: `server.New(cfg)` and the unexported `srv.router` field (`chi.Router`); `docs/api/openapi.yaml` at relative path `../../docs/api/openapi.yaml`.
- Produces: `TestOpenAPISpecMatchesRoutes` (drift gate) and file-level vars `exemptRoutes map[string]string`, `exemptPrefixes map[string]string`, helper `specRoutes` — Task 3 adds a second test to this same file, Task 4 invokes both via `-run 'TestOpenAPI'`.

- [ ] **Step 1: Write the failing test**

Create `internal/server/openapi_drift_test.go` with exactly this content:

```go
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
	method, path, found := strings.Cut(route, " ")
	if !found {
		return ""
	}
	if _, ok := exemptRoutes["ALL "+path]; ok {
		return "ALL " + path
	}
	_ = method
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
```

- [ ] **Step 2: Run `go mod tidy`**

The new import makes `go.yaml.in/yaml/v3` a direct dependency.

Run: `go mod tidy && git diff go.mod`
Expected: `go.yaml.in/yaml/v3 v3.0.5` moved out of the `// indirect` block.

- [ ] **Step 3: Run the test to verify it fails with the known drift**

Run: `go test ./internal/server/ -run TestOpenAPISpecMatchesRoutes -count=1`
Expected: FAIL — `missingFromSpec` lists exactly these 7 routes:

```
GET /sandboxes/{sandboxID}/ws
POST /v2/sandboxes/{sandboxID}/filesystem/list
POST /v2/sandboxes/{sandboxID}/filesystem/mkdir
POST /v2/sandboxes/{sandboxID}/filesystem/move
POST /v2/sandboxes/{sandboxID}/filesystem/rm
POST /v2/sandboxes/{sandboxID}/filesystem/upload
GET /v2/sandboxes/{sandboxID}/filesystem/download
```

No ghosts, no stale exemptions. If the failure list differs, re-inventory `internal/server/http.go` before continuing (the route table may have moved since this plan was written).

- [ ] **Step 4: Commit**

```bash
git add internal/server/openapi_drift_test.go go.mod go.sum
git commit -s -m "test(api): add chi route vs openapi.yaml drift inventory test

Walks the registered chi routes and diffs them against
docs/api/openapi.yaml in both directions, with a self-checking
exemption list for internal and legacy endpoints. Currently red:
it exposes the /v2 filesystem and /ws spec gaps.

Refs #118"
```

---

### Task 2: Close the spec gaps (green)

**Files:**
- Modify: `docs/api/openapi.yaml` (tags list ~line 58; new path entries in the `/v2` section after `/v2/sandboxes/{sandboxID}/metrics`, ~line 1085; new `/ws` entry near the Code Execution section, after `/sandboxes/{sandboxID}/code` ~line 524)

**Interfaces:**
- Consumes: existing `components/parameters/sandboxID`, `components/responses/{BadRequest,Unauthorized,InternalServerError}`; existing schemas `FileListRequest`, `FileListResponse`, `MakeDirRequest`, `FileRemoveRequest`, `MoveFileRequest`.
- Produces: spec entries for `GET /sandboxes/{sandboxID}/ws` and the 6 `/v2/sandboxes/{sandboxID}/filesystem/*` operations, making Task 1's test pass. New operationIds: `executeCodeStream`, `uploadFileV2`, `downloadFileV2`, `listFilesV2`, `makeDirV2`, `removeFileV2`, `moveFileV2`.

- [ ] **Step 1: Add the `/ws` operation**

Insert after the `/sandboxes/{sandboxID}/code` path item (before `/sandboxes/{sandboxID}/snapshots`). The `x-websocket` extension marks it as a protocol-upgrade endpoint rather than a plain REST call:

```yaml
  /sandboxes/{sandboxID}/ws:
    parameters:
      - $ref: "#/components/parameters/sandboxID"
    get:
      tags: [Code Execution]
      operationId: executeCodeStream
      summary: Streaming code execution (WebSocket / ConnectRPC)
      description: >
        Upgrade endpoint used by the E2B SDK for interactive command and code
        execution streaming. Speaks ConnectRPC and WebSocket in dual mode;
        message payloads are defined by the E2B envd protocol rather than
        this document.
      x-websocket: true
      responses:
        "101":
          description: Switching Protocols — connection upgraded
        "400":
          $ref: "#/components/responses/BadRequest"
        "401":
          $ref: "#/components/responses/Unauthorized"
        "500":
          $ref: "#/components/responses/InternalServerError"
```

- [ ] **Step 2: Add the `v2 Filesystem` tag**

In the top-level `tags:` list, after `- name: v2 Metrics`, add:

```yaml
  - name: v2 Filesystem
    description: v2 file operations inside sandboxes
```

- [ ] **Step 3: Add the six `/v2` filesystem path items**

Insert after the `/v2/sandboxes/{sandboxID}/metrics` path item (before the `# v2 Templates` banner comment). OpenAPI 3.0.3 cannot `$ref` path items, so these mirror the root-level filesystem entries with `V2` operationIds:

```yaml
  /v2/sandboxes/{sandboxID}/filesystem/upload:
    parameters:
      - $ref: "#/components/parameters/sandboxID"
    post:
      tags: [v2 Filesystem]
      operationId: uploadFileV2
      summary: Upload a file (v2)
      description: Uploads a file into the sandbox.
      parameters:
        - name: path
          in: query
          required: true
          schema:
            type: string
          description: Destination path inside the sandbox
      requestBody:
        required: true
        content:
          application/octet-stream:
            schema:
              type: string
              format: binary
      responses:
        "201":
          description: File uploaded
        "400":
          $ref: "#/components/responses/BadRequest"

  /v2/sandboxes/{sandboxID}/filesystem/download:
    parameters:
      - $ref: "#/components/parameters/sandboxID"
    get:
      tags: [v2 Filesystem]
      operationId: downloadFileV2
      summary: Download a file (v2)
      description: Downloads a file from the sandbox. Path is specified via the `path` query parameter.
      parameters:
        - name: path
          in: query
          required: true
          schema:
            type: string
          description: Absolute path to the file inside the sandbox
      responses:
        "200":
          description: File content
          content:
            application/octet-stream:
              schema:
                type: string
                format: binary
        "404":
          $ref: "#/components/responses/NotFound"

  /v2/sandboxes/{sandboxID}/filesystem/list:
    parameters:
      - $ref: "#/components/parameters/sandboxID"
    post:
      tags: [v2 Filesystem]
      operationId: listFilesV2
      summary: List directory contents (v2)
      description: Lists files and directories inside a sandbox directory.
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/FileListRequest"
      responses:
        "200":
          description: Directory listing
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/FileListResponse"
        "404":
          $ref: "#/components/responses/NotFound"

  /v2/sandboxes/{sandboxID}/filesystem/mkdir:
    parameters:
      - $ref: "#/components/parameters/sandboxID"
    post:
      tags: [v2 Filesystem]
      operationId: makeDirV2
      summary: Create a directory (v2)
      description: Creates a directory inside the sandbox.
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/MakeDirRequest"
      responses:
        "201":
          description: Directory created
        "400":
          $ref: "#/components/responses/BadRequest"

  /v2/sandboxes/{sandboxID}/filesystem/rm:
    parameters:
      - $ref: "#/components/parameters/sandboxID"
    post:
      tags: [v2 Filesystem]
      operationId: removeFileV2
      summary: Remove a file or directory (v2)
      description: Removes a file or directory inside the sandbox.
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/FileRemoveRequest"
      responses:
        "204":
          description: Removed successfully
        "404":
          $ref: "#/components/responses/NotFound"

  /v2/sandboxes/{sandboxID}/filesystem/move:
    parameters:
      - $ref: "#/components/parameters/sandboxID"
    post:
      tags: [v2 Filesystem]
      operationId: moveFileV2
      summary: Move or rename a file (v2)
      description: Moves or renames a file or directory inside the sandbox.
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/MoveFileRequest"
      responses:
        "204":
          description: File moved
        "404":
          $ref: "#/components/responses/NotFound"
```

- [ ] **Step 4: Run the drift test to verify it passes**

Run: `go test ./internal/server/ -run TestOpenAPISpecMatchesRoutes -count=1`
Expected: PASS

- [ ] **Step 5: Validate the YAML parses cleanly**

Run: `go run - <<'EOF'` is overkill; instead re-run the parser through the test plus a quick syntax check:

```bash
python3 -c "import yaml,sys; yaml.safe_load(open('docs/api/openapi.yaml')); print('yaml ok')"
```

Expected: `yaml ok` (if PyYAML is unavailable locally, rely on the Go test — it fails on any parse error).

- [ ] **Step 6: Commit**

```bash
git add docs/api/openapi.yaml
git commit -s -m "docs(api): document /v2 filesystem routes and sandbox ws endpoint

Closes the spec gaps found by the route inventory test: six /v2
filesystem operations mirroring the root-level ones, and the
streaming /ws upgrade endpoint marked with x-websocket.

Refs #118"
```

---

### Task 3: Error-response schema quality gate

**Files:**
- Modify: `internal/server/openapi_drift_test.go` (append `TestOpenAPIErrorResponses` + helpers)
- Modify: `docs/api/openapi.yaml` (add standard error responses to 18 operations)

**Interfaces:**
- Consumes: `openAPIPath`, `httpMethods` from Task 1's file; spec components `responses/{Unauthorized,InternalServerError,...}` and `schemas/{Error,AuthError}`.
- Produces: `TestOpenAPIErrorResponses` — every documented operation except health probes must have at least one `>=400`/`default` response referencing the shared error schemas (directly or via a `components/responses` entry that itself references them).

- [ ] **Step 1: Write the failing test**

Append to `internal/server/openapi_drift_test.go`:

```go
// errorResponseExemptOps are operations allowed to document no error
// response: liveness/readiness probes return plain status codes.
var errorResponseExemptOps = map[string]bool{
	"GET /healthz": true,
	"GET /readyz":  true,
}

// errorSchemaRefs matches $ref targets that count as a documented error
// response body.
func isErrorSchemaRef(s string) bool {
	return s == "#/components/schemas/Error" || s == "#/components/schemas/AuthError"
}

// refsErrorSchema walks a decoded YAML value and reports whether it contains
// a $ref to a shared error schema anywhere below it.
func refsErrorSchema(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == "$ref" {
				if s, ok := val.(string); ok && isErrorSchemaRef(s) {
					return true
				}
			}
			if refsErrorSchema(val) {
				return true
			}
		}
	case []any:
		for _, e := range t {
			if refsErrorSchema(e) {
				return true
			}
		}
	}
	return false
}

// errorResponseComponents returns the names under components/responses whose
// bodies reference a shared error schema.
func errorResponseComponents(doc map[string]any) map[string]bool {
	names := map[string]bool{}
	comps, _ := doc["components"].(map[string]any)
	resps, _ := comps["responses"].(map[string]any)
	for name, r := range resps {
		if refsErrorSchema(r) {
			names[name] = true
		}
	}
	return names
}

// opHasErrorResponse reports whether an operation documents at least one
// error response (status >= 400 or "default") whose body resolves to a
// shared error schema.
func opHasErrorResponse(op any, errComps map[string]bool) bool {
	m, ok := op.(map[string]any)
	if !ok {
		return false
	}
	responses, _ := m["responses"].(map[string]any)
	for status, resp := range responses {
		isErr := status == "default" || (len(status) == 3 && status[0] >= '4' && status[0] <= '5')
		if !isErr {
			continue
		}
		if rm, ok := resp.(map[string]any); ok {
			if ref, ok := rm["$ref"].(string); ok {
				if name := strings.TrimPrefix(ref, "#/components/responses/"); errComps[name] {
					return true
				}
			}
		}
		if refsErrorSchema(resp) {
			return true
		}
	}
	return false
}

// TestOpenAPIErrorResponses fails when an operation documents no error
// response referencing components/schemas/Error or AuthError (directly or via
// a shared components/responses entry), so error bodies stay consistent with
// the design doc's Error Codes section.
func TestOpenAPIErrorResponses(t *testing.T) {
	data, err := os.ReadFile(openAPIPath)
	if err != nil {
		t.Fatalf("reading %s: %v", openAPIPath, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing %s: %v", openAPIPath, err)
	}
	errComps := errorResponseComponents(doc)
	if len(errComps) == 0 {
		t.Fatalf("no components/responses reference the shared error schemas; cannot gate")
	}

	paths, _ := doc["paths"].(map[string]any)
	var violations []string
	for path, item := range paths {
		pi, _ := item.(map[string]any)
		for method, op := range pi {
			if !httpMethods[strings.ToLower(method)] {
				continue
			}
			key := strings.ToUpper(method) + " " + path
			if errorResponseExemptOps[key] {
				continue
			}
			if !opHasErrorResponse(op, errComps) {
				violations = append(violations, key)
			}
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Errorf("operations without an error response referencing Error/AuthError (add e.g. \"401\": $ref Unauthorized and \"500\": $ref InternalServerError):\n  %s",
			strings.Join(violations, "\n  "))
	}
}
```

- [ ] **Step 2: Run the test to verify it fails with the 18 known violations**

Run: `go test ./internal/server/ -run TestOpenAPIErrorResponses -count=1`
Expected: FAIL listing exactly:

```
GET /sandboxes/{sandboxID}/logs
GET /sandboxes/{sandboxID}/ports
GET /templates
GET /v2/sandboxes
GET /v2/sandboxes/{sandboxID}/logs
GET /v2/templates
GET /warm-pools
POST /sandboxes/{sandboxID}/access-token
POST /sandboxes/{sandboxID}/snapshots
POST /templates
POST /templates/{templateID}/aliases
POST /v2/templates
POST /v2/templates/{templateID}/tags
POST /warm-pools
GET /sandboxes/{sandboxID}/snapshots
GET /templates/{templateID}/tags
GET /v2/templates/{templateID}/tags
POST /templates/{templateID}/tags
```

(18 operations; sorted output may interleave GET/POST differently — the set must match. If it doesn't, re-audit before fixing.)

- [ ] **Step 3: Fix the spec — add the standard error block to each violating operation**

For each of the 18 operations, append to its `responses:` map (keeping existing success entries), matching the indentation of sibling response keys (8 spaces):

```yaml
        "401":
          $ref: "#/components/responses/Unauthorized"
        "500":
          $ref: "#/components/responses/InternalServerError"
```

For operations where 404 is semantically right (single-resource GETs like `GET /sandboxes/{sandboxID}/logs`, `GET /v2/sandboxes/{sandboxID}/logs`, `GET /sandboxes/{sandboxID}/ports` is a list — skip those), prefer adding `"404": $ref: "#/components/responses/NotFound"` **in addition**, not instead. Keep it minimal: the gate only requires one error response; 401+500 everywhere is the uniform baseline.

- [ ] **Step 4: Run both OpenAPI tests to verify they pass**

Run: `go test ./internal/server/ -run 'TestOpenAPI' -count=1`
Expected: PASS (both `TestOpenAPISpecMatchesRoutes` and `TestOpenAPIErrorResponses`)

- [ ] **Step 5: Commit**

```bash
git add internal/server/openapi_drift_test.go docs/api/openapi.yaml
git commit -s -m "docs(api): reference shared Error schema in all operation responses

Adds a CI gate (TestOpenAPIErrorResponses) requiring every documented
operation except health probes to reference components/schemas/Error
or AuthError through at least one error response, and fills the
18 operations that were missing one.

Refs #118"
```

---

### Task 4: `make openapi-check`, CI wiring, PR template

**Files:**
- Modify: `Makefile` (Testing section, after `test-e2e` ~line 49)
- Modify: `.github/workflows/check.yml` (test job)
- Create: `.github/pull_request_template.md`

**Interfaces:**
- Consumes: `TestOpenAPISpecMatchesRoutes` + `TestOpenAPIErrorResponses` from Tasks 1–3 (selected via `-run 'TestOpenAPI'`).
- Produces: `make openapi-check` target used by contributors and the `check` workflow on every PR.

- [ ] **Step 1: Add the Makefile target**

After the `test-e2e` block in the `##@ Testing` section:

```make
.PHONY: openapi-check
openapi-check: ## Verify openapi.yaml matches the registered routes.
	$(GOTEST) -count=1 -run 'TestOpenAPI' ./internal/server/
```

- [ ] **Step 2: Verify the target works**

Run: `make openapi-check`
Expected: `ok github.com/e2bgateway/e2bgateway/internal/server`

- [ ] **Step 3: Wire into CI**

In `.github/workflows/check.yml`, in the `test` job, add a step before `Run tests`:

```yaml
      - name: OpenAPI drift check
        run: make openapi-check
```

(The workflow already runs on every PR against main/master, so this covers the "all PRs" requirement — no new workflow file needed.)

- [ ] **Step 4: Create the PR template**

Create `.github/pull_request_template.md`:

```markdown
## What does this PR do?

<!-- One or two sentences describing the change. -->

## Related issues

<!-- e.g. Closes #123 -->

## Checklist

- [ ] Tests written first (TDD); each new test was watched failing before the implementation landed
- [ ] Design doc committed: `docs/superpowers/specs/YYYY-MM-DD-<topic>-design.md` (trivial changes must state why they are exempt)
- [ ] `make pre-commit` passes (fmt + vet + lint + test)
- [ ] New or changed HTTP routes are reflected in `docs/api/openapi.yaml` — `make openapi-check` passes. Internal-only routes must be added to the exemption list in `internal/server/openapi_drift_test.go` with a reason.
- [ ] Docs updated if behavior or config changed (`AGENTS.md`, `configs/e2bgateway-example.yaml`)
- [ ] Commit messages follow Conventional Commits (see `.agents/skills/submit-pr`)
```

- [ ] **Step 5: Run the full pre-commit gate**

Run: `make pre-commit`
Expected: fmt/vet/lint/test all pass (the drift tests also run inside `go test ./...`).

- [ ] **Step 6: Commit**

```bash
git add Makefile .github/workflows/check.yml .github/pull_request_template.md
git commit -s -m "ci: add make openapi-check to PR checks and PR template reminder

Runs the openapi.yaml route-drift and error-schema gates on every PR
via the existing check workflow, and reminds contributors to update
the spec when adding routes.

Closes #118"
```

---

## Self-Review Notes

- **Spec coverage** (issue #118 checkboxes): 路由盘点测试 → Task 1 (both drift directions + exemption list + stale-exemption check); 补全缺口 → Task 2 (inventory-verified: only /v2 filesystem and /ws were actually missing — v2 snapshots/tenants don't exist in the router, warm-pool force and logs are already documented); schema 质量 → Task 3 (Error refs gate; `x-websocket` extension in Task 2); `make openapi-check` + CI + PR template → Task 4. Optional website rendering (#101) is out of scope — depends on a repo that doesn't exist yet.
- **Type consistency:** `openAPIPath`, `httpMethods`, `exemptRoutes`, `exemptPrefixes`, `exemptRule`, `specRoutes`, `registeredRoutes`, `newDriftTestServer` defined in Task 1 and reused in Task 3; test names both start with `TestOpenAPI` matching Task 4's `-run 'TestOpenAPI'`.
- **Risks flagged inline:** Task 1 Step 3 and Task 3 Step 2 list exact expected failure sets — if they differ, re-inventory before proceeding (the router may have changed since 2026-11-26).
