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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
)

// contractExemptOps are operations the contract test cannot exercise over
// plain HTTP: the streaming upgrade endpoint speaks the envd protocol.
var contractExemptOps = map[string]bool{
	"GET /sandboxes/{sandboxID}/ws": true,
}

// contractRunner sends requests to the mock-backed router and validates both
// directions against openapi.yaml: the outgoing request must satisfy the
// documented contract, and the server's response must match one of the
// documented responses for that operation.
type contractRunner struct {
	t       *testing.T
	handler http.Handler
	swagger *openapi3.T
	opts    *openapi3filter.Options
	visited map[string]bool
}

func newContractRunner(t *testing.T, handler http.Handler) *contractRunner {
	t.Helper()
	loader := openapi3.NewLoader()
	swagger, err := loader.LoadFromFile(openAPIPath)
	if err != nil {
		t.Fatalf("loading %s: %v", openAPIPath, err)
	}
	if err := swagger.Validate(context.Background()); err != nil {
		t.Fatalf("validating %s: %v", openAPIPath, err)
	}
	return &contractRunner{
		t:       t,
		handler: handler,
		swagger: swagger,
		opts: &openapi3filter.Options{
			// The test server runs without auth providers; accept any
			// credentials so the gate checks payloads, not security.
			AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
			// Fail on status codes the operation does not document —
			// without this, an undocumented 200 passes silently.
			IncludeResponseStatus: true,
		},
		visited: map[string]bool{},
	}
}

// call executes one operation: validate the request against the spec, serve
// it, validate the response against the spec. It returns the response body
// so callers can extract resource IDs for subsequent calls.
func (c *contractRunner) call(method, pathTmpl string, pathParams, query map[string]string, contentType string, body []byte) []byte {
	c.t.Helper()

	path := pathTmpl
	for name, val := range pathParams {
		path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(val))
	}
	target := "http://contract.test" + path
	if len(query) > 0 {
		q := url.Values{}
		for k, v := range query {
			q.Set(k, v)
		}
		target += "?" + q.Encode()
	}

	pathItem := c.swagger.Paths.Find(pathTmpl)
	if pathItem == nil {
		c.t.Fatalf("path %s not found in %s", pathTmpl, openAPIPath)
	}
	op := pathItem.GetOperation(strings.ToUpper(method))
	if op == nil {
		c.t.Fatalf("operation %s %s not found in %s", method, pathTmpl, openAPIPath)
	}
	route := &routers.Route{
		Spec:      c.swagger,
		Path:      pathTmpl,
		PathItem:  pathItem,
		Method:    strings.ToUpper(method),
		Operation: op,
	}

	buildReq := func() *http.Request {
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req := httptest.NewRequest(method, target, rdr)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		return req
	}

	// ValidateRequest consumes the body, so validate a separate copy from
	// the one that is served.
	reqInput := &openapi3filter.RequestValidationInput{
		Request:    buildReq(),
		PathParams: pathParams,
		Route:      route,
		Options:    c.opts,
	}
	if err := openapi3filter.ValidateRequest(context.Background(), reqInput); err != nil {
		c.t.Errorf("%s %s: request violates the spec: %v", method, pathTmpl, err)
	}

	rec := httptest.NewRecorder()
	c.handler.ServeHTTP(rec, buildReq())

	respInput := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: reqInput,
		Status:                 rec.Code,
		Header:                 rec.Header(),
		Options:                c.opts,
	}
	respInput.SetBodyBytes(rec.Body.Bytes())
	if err := openapi3filter.ValidateResponse(context.Background(), respInput); err != nil {
		c.t.Errorf("%s %s: response %d violates the spec: %v\nbody: %s",
			method, pathTmpl, rec.Code, err, rec.Body.String())
	}

	c.visited[strings.ToUpper(method)+" "+pathTmpl] = true
	return rec.Body.Bytes()
}

// callJSON is call with a JSON request body.
func (c *contractRunner) callJSON(method, pathTmpl string, pathParams, query map[string]string, body any) []byte {
	c.t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshaling request body: %v", err)
		}
	}
	return c.call(method, pathTmpl, pathParams, query, "application/json", raw)
}

// checkCoverage fails when a documented operation was never exercised.
func (c *contractRunner) checkCoverage() {
	c.t.Helper()
	var uncovered []string
	for path, item := range c.swagger.Paths.Map() {
		for method, op := range item.Operations() {
			key := strings.ToUpper(method) + " " + path
			if contractExemptOps[key] || op.Extensions["x-websocket"] != nil {
				continue
			}
			if !c.visited[key] {
				uncovered = append(uncovered, key)
			}
		}
	}
	if len(uncovered) > 0 {
		c.t.Errorf("spec operations not exercised by the contract test:\n  %s", strings.Join(uncovered, "\n  "))
	}
}

// multipartBody builds a multipart/form-data payload with one file part and
// optional extra form fields.
func multipartBody(t *testing.T, fieldName, filename, content string, fields map[string]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("writing form field: %v", err)
		}
	}
	part, err := w.CreateFormFile(fieldName, filename)
	if err != nil {
		t.Fatalf("creating form file: %v", err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatalf("writing form file content: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

// TestOpenAPIContract exercises every documented operation against the
// mock-backed router and validates requests and responses against
// docs/api/openapi.yaml. It catches semantic drift that the path-level
// inventory cannot see: wrong status codes, wrong request formats, response
// bodies that do not match their schemas.
func TestOpenAPIContract(t *testing.T) {
	srv := newDriftTestServer(t)
	c := newContractRunner(t, srv.Handler())

	// -- Health --
	c.call("GET", "/healthz", nil, nil, "", nil)
	c.call("GET", "/readyz", nil, nil, "", nil)
	c.call("GET", "/versionz", nil, nil, "", nil)

	// -- Sandbox lifecycle --
	var created struct {
		SandboxID string `json:"sandboxID"`
	}
	mustUnmarshal(t, c.callJSON("POST", "/sandboxes", nil, nil,
		map[string]any{"templateID": "base", "timeout": 300}), &created)
	if created.SandboxID == "" {
		t.Fatal("create sandbox returned no sandboxID")
	}
	sbx := map[string]string{"sandboxID": created.SandboxID}

	c.call("GET", "/sandboxes", nil, nil, "", nil)
	c.call("GET", "/v2/sandboxes", nil, map[string]string{"limit": "10"}, "", nil)
	c.call("GET", "/sandboxes/{sandboxID}", sbx, nil, "", nil)
	c.callJSON("POST", "/sandboxes/{sandboxID}/timeout", sbx, nil, map[string]any{"timeout": 600})
	c.callJSON("PATCH", "/sandboxes/{sandboxID}/timeout", sbx, nil, map[string]any{"timeout": 600})
	c.callJSON("POST", "/sandboxes/{sandboxID}/envs", sbx, nil, map[string]any{"envs": map[string]string{"A": "b"}})
	c.call("POST", "/sandboxes/{sandboxID}/pause", sbx, nil, "", nil)
	c.callJSON("POST", "/sandboxes/{sandboxID}/resume", sbx, nil, map[string]any{"timeout": 300})

	// -- Filesystem (v1 + v2, JSON and multipart upload) --
	for _, prefix := range []string{"", "/v2"} {
		c.callJSON("POST", prefix+"/sandboxes/{sandboxID}/filesystem/upload", sbx, nil,
			map[string]any{"path": "/tmp/hello.txt", "content": "hi"})
		fileBody, fileCT := multipartBody(t, "file", "uploaded.txt", "data",
			map[string]string{"path": "/tmp/uploaded.txt"})
		c.call("POST", prefix+"/sandboxes/{sandboxID}/filesystem/upload", sbx, nil, fileCT, fileBody)
		c.call("GET", prefix+"/sandboxes/{sandboxID}/filesystem/download", sbx,
			map[string]string{"path": "/tmp/hello.txt"}, "", nil)
		c.callJSON("POST", prefix+"/sandboxes/{sandboxID}/filesystem/list", sbx, nil,
			map[string]any{"path": "/"})
		c.callJSON("POST", prefix+"/sandboxes/{sandboxID}/filesystem/mkdir", sbx, nil,
			map[string]any{"path": "/tmp/dir"})
		c.callJSON("POST", prefix+"/sandboxes/{sandboxID}/filesystem/move", sbx, nil,
			map[string]any{"source": "/tmp/hello.txt", "destination": "/tmp/moved.txt"})
		c.callJSON("POST", prefix+"/sandboxes/{sandboxID}/filesystem/rm", sbx, nil,
			map[string]any{"path": "/tmp/moved.txt"})
	}

	// -- Commands and processes --
	c.callJSON("POST", "/sandboxes/{sandboxID}/commands", sbx, nil,
		map[string]any{"command": "echo hi"})
	c.call("GET", "/sandboxes/{sandboxID}/commands", sbx, nil, "", nil)
	// The mock backend does not track spawned processes, so kill/input hit
	// the documented not-found path; the response must still match the spec.
	proc := map[string]string{"sandboxID": created.SandboxID, "processID": "proc-1"}
	c.call("POST", "/sandboxes/{sandboxID}/commands/{processID}/kill", proc, nil, "", nil)
	c.callJSON("POST", "/sandboxes/{sandboxID}/commands/{processID}/input", proc, nil,
		map[string]any{"data": "exit\n"})

	// -- Code execution --
	c.callJSON("POST", "/sandboxes/{sandboxID}/code", sbx, nil,
		map[string]any{"code": "print(1)", "language": "python"})

	// -- Snapshots, ports, tokens, logs, metrics --
	c.callJSON("POST", "/sandboxes/{sandboxID}/snapshots", sbx, nil,
		map[string]any{"name": "snap-1", "description": "contract test"})
	c.call("GET", "/sandboxes/{sandboxID}/snapshots", sbx, nil, "", nil)
	c.call("GET", "/sandboxes/{sandboxID}/ports", sbx, nil, "", nil)
	c.call("GET", "/sandboxes/{sandboxID}/ports/{port}",
		map[string]string{"sandboxID": created.SandboxID, "port": "3000"}, nil, "", nil)
	c.call("POST", "/sandboxes/{sandboxID}/access-token", sbx, nil, "", nil)
	c.call("GET", "/sandboxes/{sandboxID}/logs", sbx, nil, "", nil)
	c.call("GET", "/v2/sandboxes/{sandboxID}/logs", sbx, nil, "", nil)
	c.call("GET", "/v2/sandboxes/{sandboxID}/metrics", sbx, nil, "", nil)

	// -- Templates --
	var build struct {
		TemplateID string `json:"templateID"`
		BuildID    string `json:"buildID"`
	}
	mustUnmarshal(t, c.callJSON("POST", "/templates", nil, nil,
		map[string]any{"name": "contract-tmpl", "dockerfile": "FROM scratch"}), &build)
	if build.TemplateID == "" {
		t.Fatal("create template returned no templateID")
	}
	tmpl := map[string]string{"templateID": build.TemplateID}

	c.call("GET", "/templates", nil, nil, "", nil)
	c.call("GET", "/templates/{templateID}", tmpl, nil, "", nil)

	var build2 struct {
		BuildID string `json:"buildID"`
	}
	mustUnmarshal(t, c.callJSON("POST", "/templates/{templateID}/builds", tmpl, nil,
		map[string]any{"name": "contract-tmpl", "dockerfile": "FROM scratch"}), &build2)
	statusParams := map[string]string{"templateID": build.TemplateID, "buildID": build2.BuildID}
	c.call("POST", "/templates/{templateID}/builds/{buildID}/status", statusParams, nil, "", nil)

	alias := map[string]string{"templateID": build.TemplateID, "alias": "contract-alias"}
	c.callJSON("POST", "/templates/{templateID}/aliases", alias, nil, map[string]any{"alias": "contract-alias"})
	c.call("DELETE", "/templates/{templateID}/aliases/{alias}", alias, nil, "", nil)

	tag := map[string]string{"templateID": build.TemplateID, "tagName": "v1"}
	c.callJSON("POST", "/templates/{templateID}/tags", tag, nil, map[string]any{"name": "v1"})
	c.call("GET", "/templates/{templateID}/tags", tmpl, nil, "", nil)
	c.call("DELETE", "/templates/{templateID}/tags/{tagName}", tag, nil, "", nil)

	c.call("DELETE", "/templates/{templateID}", tmpl, nil, "", nil)

	// -- v2 templates --
	c.call("GET", "/v2/templates", nil, nil, "", nil)
	var v2tmpl struct {
		TemplateID string `json:"templateID"`
	}
	mustUnmarshal(t, c.callJSON("POST", "/v2/templates", nil, nil,
		map[string]any{"name": "contract-v2-tmpl", "dockerfile": "FROM scratch"}), &v2tmpl)
	if v2tmpl.TemplateID == "" {
		t.Fatal("create v2 template returned no templateID")
	}
	v2p := map[string]string{"templateID": v2tmpl.TemplateID}
	c.callJSON("PATCH", "/v2/templates/{templateID}", v2p, nil, map[string]any{"name": "renamed"})

	v2tag := map[string]string{"templateID": v2tmpl.TemplateID, "tagName": "vt"}
	c.callJSON("POST", "/v2/templates/{templateID}/tags", v2tag, nil, map[string]any{"name": "vt"})
	c.call("GET", "/v2/templates/{templateID}/tags", v2p, nil, "", nil)
	c.call("DELETE", "/v2/templates/{templateID}/tags/{tagName}", v2tag, nil, "", nil)

	// -- Warm pools --
	var pool struct {
		WarmPoolID string `json:"warmPoolID"`
	}
	mustUnmarshal(t, c.callJSON("POST", "/warm-pools", nil, nil,
		map[string]any{"templateID": "base", "targetSize": 2}), &pool)
	if pool.WarmPoolID == "" {
		t.Fatal("create warm pool returned no warmPoolID")
	}
	wp := map[string]string{"warmPoolID": pool.WarmPoolID}
	c.call("GET", "/warm-pools", nil, nil, "", nil)
	c.call("GET", "/warm-pools/{warmPoolID}", wp, nil, "", nil)
	c.callJSON("POST", "/warm-pools/{warmPoolID}/size", wp, nil, map[string]any{"targetSize": 3})
	c.call("DELETE", "/warm-pools/{warmPoolID}", wp, map[string]string{"force": "true"}, "", nil)

	// -- Kill the sandbox last so the fixtures above keep working --
	c.call("DELETE", "/sandboxes/{sandboxID}", sbx, nil, "", nil)

	c.checkCoverage()
}

func mustUnmarshal(t *testing.T, data []byte, v any) {
	t.Helper()
	if len(data) == 0 {
		t.Fatalf("expected a JSON response body, got empty")
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("decoding response %q: %v", string(data), err)
	}
}
