// Package e2e provides end-to-end tests for the E2B API compatibility layer.
// These tests verify that the gateway exposes the exact same API contract as the
// official E2B API so that E2B SDKs and CLI work against E2BGateway.
package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e2bgateway/e2bgateway/internal/api/dto"
	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/server"
)

// testServer sets up an E2BGateway server backed by the mock adapter.
func testServer(t *testing.T) *httptest.Server {
	t.Helper()

	cfg := &config.Config{
		Server: config.ServerConfig{
			HTTP: config.HTTPConfig{Address: "127.0.0.1:0"},
		},
		Backends: []config.BackendConfig{
			{Name: "mock", Type: "mock", Enabled: true},
		},
		Routing: config.RoutingConfig{
			DefaultBackend: "mock",
			Strategy:       "static",
		},
	}

	srv, err := server.New(cfg)
	if err != nil {
		t.Fatalf("creating server: %v", err)
	}

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func doJSON(t *testing.T, ts *httptest.Server, method, path string, body interface{}) *http.Response {
	t.Helper()

	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshaling body: %v", err)
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, ts.URL+path, reqBody)
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "test-api-key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("executing request: %v", err)
	}
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response, target interface{}) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
}

// ----- E2E Test: Create Sandbox returns E2B format -----

func TestE2E_CreateSandbox_E2BFormat(t *testing.T) {
	ts := testServer(t)

	req := dto.SandboxCreateRequest{
		TemplateID: "base",
		Timeout:    300,
		Metadata:   map[string]string{"user": "test"},
	}
	resp := doJSON(t, ts, http.MethodPost, "/sandboxes", req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var result dto.SandboxCreateResponse
	decodeJSON(t, resp, &result)

	if result.SandboxID == "" {
		t.Error("expected non-empty sandboxID")
	}
	if result.TemplateID != "base" {
		t.Errorf("expected templateID 'base', got %s", result.TemplateID)
	}
	if result.EnvdVersion == "" {
		t.Error("expected non-empty envdVersion (E2B SDK requires it)")
	}
	if result.EnvdAccessToken == "" {
		t.Error("expected non-empty envdAccessToken (E2B SDK requires it)")
	}
}

// ----- E2E Test: List Sandboxes returns E2B format with state field -----

func TestE2E_ListSandboxes_E2BFormat(t *testing.T) {
	ts := testServer(t)

	// Create a sandbox
	createResp := doJSON(t, ts, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", createResp.StatusCode)
	}
	createResp.Body.Close()

	// List sandboxes
	listResp := doJSON(t, ts, http.MethodGet, "/sandboxes", nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", listResp.StatusCode)
	}

	var sandboxes []dto.SandboxInfo
	decodeJSON(t, listResp, &sandboxes)

	if len(sandboxes) < 1 {
		t.Fatal("expected at least 1 sandbox")
	}

	// Verify E2B format
	sb := sandboxes[0]
	if sb.SandboxID == "" {
		t.Error("expected non-empty sandboxID")
	}
	if sb.State == "" {
		t.Error("expected non-empty 'state' field (E2B uses 'state' not 'status')")
	}
	if sb.State != "running" {
		t.Errorf("expected state 'running', got %s", sb.State)
	}
}

// ----- E2E Test: Get Sandbox returns E2B format -----

func TestE2E_GetSandbox_E2BFormat(t *testing.T) {
	ts := testServer(t)

	// Create a sandbox
	createResp := doJSON(t, ts, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	var created dto.SandboxCreateResponse
	decodeJSON(t, createResp, &created)

	// Get sandbox
	getResp := doJSON(t, ts, http.MethodGet, "/sandboxes/"+created.SandboxID, nil)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get: expected 200, got %d", getResp.StatusCode)
	}

	var sandbox dto.SandboxInfo
	decodeJSON(t, getResp, &sandbox)

	if sandbox.SandboxID != created.SandboxID {
		t.Errorf("expected sandboxID %s, got %s", created.SandboxID, sandbox.SandboxID)
	}
	if sandbox.State != "running" {
		t.Errorf("expected state 'running', got %s", sandbox.State)
	}
}

// ----- E2E Test: Error format is E2B compatible -----

func TestE2E_ErrorFormat(t *testing.T) {
	ts := testServer(t)

	resp := doJSON(t, ts, http.MethodGet, "/sandboxes/nonexistent-sandbox-id", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}

	var errResp dto.ErrorResponse
	decodeJSON(t, resp, &errResp)

	// E2B error format: {"code": int, "message": string}
	if errResp.Code != 404 {
		t.Errorf("expected error code 404 (int), got %d", errResp.Code)
	}
	if errResp.Message == "" {
		t.Error("expected non-empty error message")
	}
}

// ----- E2E Test: Pause and Resume -----

func TestE2E_PauseResume(t *testing.T) {
	ts := testServer(t)

	// Create a sandbox
	createResp := doJSON(t, ts, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	var created dto.SandboxCreateResponse
	decodeJSON(t, createResp, &created)

	// Pause sandbox
	pauseResp := doJSON(t, ts, http.MethodPost, "/sandboxes/"+created.SandboxID+"/pause", nil)
	if pauseResp.StatusCode != http.StatusNoContent {
		t.Fatalf("pause: expected 204, got %d", pauseResp.StatusCode)
	}
	pauseResp.Body.Close()

	// Resume sandbox
	resumeResp := doJSON(t, ts, http.MethodPost, "/sandboxes/"+created.SandboxID+"/resume", nil)
	if resumeResp.StatusCode != http.StatusOK {
		t.Fatalf("resume: expected 200, got %d", resumeResp.StatusCode)
	}

	var resumed dto.SandboxInfo
	decodeJSON(t, resumeResp, &resumed)
	if resumed.SandboxID != created.SandboxID {
		t.Errorf("expected sandboxID %s, got %s", created.SandboxID, resumed.SandboxID)
	}
}

// ----- E2E Test: Set Timeout -----

func TestE2E_SetTimeout(t *testing.T) {
	ts := testServer(t)

	createResp := doJSON(t, ts, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	var created dto.SandboxCreateResponse
	decodeJSON(t, createResp, &created)

	resp := doJSON(t, ts, http.MethodPost, "/sandboxes/"+created.SandboxID+"/timeout", dto.SandboxTimeoutRequest{Timeout: 600})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// ----- E2E Test: Execute Code -----

func TestE2E_ExecuteCode(t *testing.T) {
	ts := testServer(t)

	createResp := doJSON(t, ts, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	var created dto.SandboxCreateResponse
	decodeJSON(t, createResp, &created)

	execResp := doJSON(t, ts, http.MethodPost, "/sandboxes/"+created.SandboxID+"/code", dto.CodeExecRequest{
		Code:     "print('hello')",
		Language: "python",
	})
	if execResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", execResp.StatusCode)
	}

	var result dto.CodeExecResult
	decodeJSON(t, execResp, &result)
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
}

// ----- E2E Test: Run Command -----

func TestE2E_RunCommand(t *testing.T) {
	ts := testServer(t)

	createResp := doJSON(t, ts, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	var created dto.SandboxCreateResponse
	decodeJSON(t, createResp, &created)

	cmdResp := doJSON(t, ts, http.MethodPost, "/sandboxes/"+created.SandboxID+"/commands", dto.CommandRequest{
		Command: "echo hello",
	})
	if cmdResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", cmdResp.StatusCode)
	}

	var result dto.CommandResult
	decodeJSON(t, cmdResp, &result)
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
}

// ----- E2E Test: File Operations -----

func TestE2E_FileOperations(t *testing.T) {
	ts := testServer(t)

	createResp := doJSON(t, ts, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	var created dto.SandboxCreateResponse
	decodeJSON(t, createResp, &created)
	sbxID := created.SandboxID

	// Write file
	writeResp := doJSON(t, ts, http.MethodPost, "/sandboxes/"+sbxID+"/files", dto.FileWriteRequest{
		Path:    "/test.txt",
		Content: "hello world",
	})
	if writeResp.StatusCode != http.StatusNoContent {
		t.Fatalf("write: expected 204, got %d", writeResp.StatusCode)
	}
	writeResp.Body.Close()

	// List files
	listResp := doJSON(t, ts, http.MethodPost, "/sandboxes/"+sbxID+"/files/list", dto.FileListRequest{Path: "/"})
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", listResp.StatusCode)
	}

	var fileList dto.FileListResponse
	decodeJSON(t, listResp, &fileList)
	if len(fileList.Entries) < 1 {
		t.Errorf("expected at least 1 file entry, got %d", len(fileList.Entries))
	}

	// Make directory
	mkdirResp := doJSON(t, ts, http.MethodPost, "/sandboxes/"+sbxID+"/files/make-dir", dto.MakeDirRequest{Path: "/testdir"})
	if mkdirResp.StatusCode != http.StatusNoContent {
		t.Fatalf("mkdir: expected 204, got %d", mkdirResp.StatusCode)
	}
	mkdirResp.Body.Close()

	// Remove file
	rmResp := doJSON(t, ts, http.MethodPost, "/sandboxes/"+sbxID+"/files/remove", dto.FileRemoveRequest{Path: "/test.txt"})
	if rmResp.StatusCode != http.StatusNoContent {
		t.Fatalf("rm: expected 204, got %d", rmResp.StatusCode)
	}
	rmResp.Body.Close()
}

// ----- E2E Test: Filesystem (envd-compatible) Paths -----

func TestE2E_Filesystem_EnvdPaths(t *testing.T) {
	ts := testServer(t)

	createResp := doJSON(t, ts, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	var created dto.SandboxCreateResponse
	decodeJSON(t, createResp, &created)
	sbxID := created.SandboxID

	// Write file via /files endpoint (adapter)
	writeResp := doJSON(t, ts, http.MethodPost, "/sandboxes/"+sbxID+"/files", dto.FileWriteRequest{
		Path:    "/test.txt",
		Content: "hello",
	})
	if writeResp.StatusCode != http.StatusNoContent {
		t.Fatalf("write: expected 204, got %d", writeResp.StatusCode)
	}
	writeResp.Body.Close()

	// List via /filesystem/list (envd-compatible path)
	listResp := doJSON(t, ts, http.MethodPost, "/sandboxes/"+sbxID+"/filesystem/list", dto.FileListRequest{Path: "/"})
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("filesystem list: expected 200, got %d", listResp.StatusCode)
	}

	var fileList dto.FileListResponse
	decodeJSON(t, listResp, &fileList)
	if len(fileList.Entries) < 1 {
		t.Error("expected at least 1 file entry from envd-compatible path")
	}

	// mkdir via /filesystem/mkdir
	mkdirResp := doJSON(t, ts, http.MethodPost, "/sandboxes/"+sbxID+"/filesystem/mkdir", dto.MakeDirRequest{Path: "/newdir"})
	if mkdirResp.StatusCode != http.StatusNoContent {
		t.Fatalf("filesystem mkdir: expected 204, got %d", mkdirResp.StatusCode)
	}
	mkdirResp.Body.Close()
}

// ----- E2E Test: Templates -----

func TestE2E_Templates(t *testing.T) {
	ts := testServer(t)

	// List templates
	listResp := doJSON(t, ts, http.MethodGet, "/templates", nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", listResp.StatusCode)
	}

	var templates []dto.TemplateInfo
	decodeJSON(t, listResp, &templates)
	// Mock adapter returns some built-in templates

	// Create template
	createResp := doJSON(t, ts, http.MethodPost, "/templates", dto.TemplateBuildRequest{
		Name:     "test-template",
		CPUCount: 2,
		MemoryMB: 512,
	})
	if createResp.StatusCode != http.StatusAccepted {
		t.Fatalf("create: expected 202, got %d", createResp.StatusCode)
	}

	var build dto.TemplateBuildResponse
	decodeJSON(t, createResp, &build)
	if build.TemplateID == "" {
		t.Error("expected non-empty templateID")
	}
	if build.BuildID == "" {
		t.Error("expected non-empty buildID")
	}
}

// ----- E2E Test: v2 List Sandboxes -----

func TestE2E_V2_ListSandboxes(t *testing.T) {
	ts := testServer(t)

	// Create a sandbox first
	createResp := doJSON(t, ts, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	createResp.Body.Close()

	// List via v2 endpoint
	listResp := doJSON(t, ts, http.MethodGet, "/v2/sandboxes", nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("v2 list: expected 200, got %d", listResp.StatusCode)
	}

	var result dto.V2SandboxListResponse
	decodeJSON(t, listResp, &result)
	if len(result.Sandboxes) < 1 {
		t.Error("expected at least 1 sandbox in v2 list")
	}
}

// ----- E2E Test: v2 List Templates -----

func TestE2E_V2_ListTemplates(t *testing.T) {
	ts := testServer(t)

	resp := doJSON(t, ts, http.MethodGet, "/v2/templates", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("v2 templates: expected 200, got %d", resp.StatusCode)
	}

	var templates []dto.V2TemplateInfo
	decodeJSON(t, resp, &templates)
	// Mock adapter may return empty list
}

// ----- E2E Test: Set Environment Variables -----

func TestE2E_SetEnvs(t *testing.T) {
	ts := testServer(t)

	createResp := doJSON(t, ts, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	var created dto.SandboxCreateResponse
	decodeJSON(t, createResp, &created)

	resp := doJSON(t, ts, http.MethodPost, "/sandboxes/"+created.SandboxID+"/envs", dto.SetEnvsRequest{
		Envs: map[string]string{"FOO": "bar", "BAZ": "qux"},
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// ----- E2E Test: Get Logs -----

func TestE2E_GetLogs(t *testing.T) {
	ts := testServer(t)

	createResp := doJSON(t, ts, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	var created dto.SandboxCreateResponse
	decodeJSON(t, createResp, &created)

	// Get logs (v1 path)
	resp := doJSON(t, ts, http.MethodGet, "/sandboxes/"+created.SandboxID+"/logs", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var logs dto.SandboxLogsResponse
	decodeJSON(t, resp, &logs)
	// Mock adapter returns empty logs, but response format should be valid
}

// ----- E2E Test: v2 Get Logs -----

func TestE2E_V2_GetLogs(t *testing.T) {
	ts := testServer(t)

	createResp := doJSON(t, ts, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	var created dto.SandboxCreateResponse
	decodeJSON(t, createResp, &created)

	resp := doJSON(t, ts, http.MethodGet, "/v2/sandboxes/"+created.SandboxID+"/logs", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var logs dto.SandboxLogsResponse
	decodeJSON(t, resp, &logs)
}

// ----- E2E Test: v2 Get Metrics -----

func TestE2E_V2_GetMetrics(t *testing.T) {
	ts := testServer(t)

	createResp := doJSON(t, ts, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	var created dto.SandboxCreateResponse
	decodeJSON(t, createResp, &created)

	resp := doJSON(t, ts, http.MethodGet, "/v2/sandboxes/"+created.SandboxID+"/metrics", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var metrics dto.SandboxMetrics
	decodeJSON(t, resp, &metrics)
}

// ----- E2E Test: File Move -----

func TestE2E_MoveFile(t *testing.T) {
	ts := testServer(t)

	createResp := doJSON(t, ts, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	var created dto.SandboxCreateResponse
	decodeJSON(t, createResp, &created)

	// Write a file first
	writeResp := doJSON(t, ts, http.MethodPost, "/sandboxes/"+created.SandboxID+"/files", dto.FileWriteRequest{
		Path:    "/src.txt",
		Content: "content",
	})
	writeResp.Body.Close()

	// Move file
	moveResp := doJSON(t, ts, http.MethodPost, "/sandboxes/"+created.SandboxID+"/filesystem/move", dto.MoveFileRequest{
		Source:      "/src.txt",
		Destination: "/dst.txt",
	})
	if moveResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", moveResp.StatusCode)
	}
	moveResp.Body.Close()
}

// ----- E2E Test: Kill Sandbox -----

func TestE2E_KillSandbox(t *testing.T) {
	ts := testServer(t)

	createResp := doJSON(t, ts, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	var created dto.SandboxCreateResponse
	decodeJSON(t, createResp, &created)

	killResp := doJSON(t, ts, http.MethodDelete, "/sandboxes/"+created.SandboxID, nil)
	if killResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", killResp.StatusCode)
	}
	killResp.Body.Close()

	// Verify it's gone
	getResp := doJSON(t, ts, http.MethodGet, "/sandboxes/"+created.SandboxID, nil)
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after kill, got %d", getResp.StatusCode)
	}
	getResp.Body.Close()
}

// ----- E2E Test: Backward compatibility with /api/v1 prefix -----

func TestE2E_BackwardCompatibility_APIv1(t *testing.T) {
	ts := testServer(t)

	// Create via /api/v1 path
	resp := doJSON(t, ts, http.MethodPost, "/api/v1/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 from /api/v1, got %d", resp.StatusCode)
	}

	var result dto.SandboxCreateResponse
	decodeJSON(t, resp, &result)
	if result.SandboxID == "" {
		t.Error("expected non-empty sandboxID from /api/v1")
	}

	// List via /api/v1 path
	listResp := doJSON(t, ts, http.MethodGet, "/api/v1/sandboxes", nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /api/v1, got %d", listResp.StatusCode)
	}
	listResp.Body.Close()
}

// ----- E2E Test: Template Tags -----

func TestE2E_TemplateTags(t *testing.T) {
	ts := testServer(t)

	// Create template first
	createResp := doJSON(t, ts, http.MethodPost, "/templates", dto.TemplateBuildRequest{
		Name:     "tag-test-template",
		CPUCount: 1,
		MemoryMB: 256,
	})
	if createResp.StatusCode != http.StatusAccepted {
		t.Fatalf("create template: expected 202, got %d", createResp.StatusCode)
	}
	var build dto.TemplateBuildResponse
	decodeJSON(t, createResp, &build)

	// Create tag
	tagResp := doJSON(t, ts, http.MethodPost, "/templates/"+build.TemplateID+"/tags", dto.CreateTagRequest{
		Name:    "v1.0",
		BuildID: build.BuildID,
	})
	if tagResp.StatusCode != http.StatusCreated {
		t.Fatalf("create tag: expected 201, got %d", tagResp.StatusCode)
	}

	var tag dto.TagInfo
	decodeJSON(t, tagResp, &tag)
	if tag.Name != "v1.0" {
		t.Errorf("expected tag name 'v1.0', got %s", tag.Name)
	}

	// List tags
	listResp := doJSON(t, ts, http.MethodGet, "/templates/"+build.TemplateID+"/tags", nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list tags: expected 200, got %d", listResp.StatusCode)
	}

	var tags []dto.TagInfo
	decodeJSON(t, listResp, &tags)
	if len(tags) < 1 {
		t.Errorf("expected at least 1 tag, got %d", len(tags))
	}

	// Delete tag
	delResp := doJSON(t, ts, http.MethodDelete, "/templates/"+build.TemplateID+"/tags/v1.0", nil)
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete tag: expected 204, got %d", delResp.StatusCode)
	}
	delResp.Body.Close()
}

// ----- E2E Test: Warm Pools -----

func TestE2E_WarmPools(t *testing.T) {
	ts := testServer(t)

	// Create warm pool
	createResp := doJSON(t, ts, http.MethodPost, "/warm-pools", dto.WarmPoolCreateRequest{
		TemplateID: "base",
		TargetSize: 2,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", createResp.StatusCode)
	}

	var pool dto.WarmPoolInfo
	decodeJSON(t, createResp, &pool)
	if pool.WarmPoolID == "" {
		t.Error("expected non-empty warmPoolID")
	}

	// List warm pools
	listResp := doJSON(t, ts, http.MethodGet, "/warm-pools", nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", listResp.StatusCode)
	}
	listResp.Body.Close()

	// Delete warm pool
	delResp := doJSON(t, ts, http.MethodDelete, "/warm-pools/"+pool.WarmPoolID, nil)
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d", delResp.StatusCode)
	}
	delResp.Body.Close()
}

// ----- E2E Test: Snapshots -----

func TestE2E_Snapshots(t *testing.T) {
	ts := testServer(t)

	createResp := doJSON(t, ts, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	var created dto.SandboxCreateResponse
	decodeJSON(t, createResp, &created)

	// Create snapshot
	snapResp := doJSON(t, ts, http.MethodPost, "/sandboxes/"+created.SandboxID+"/snapshots", dto.SnapshotCreateRequest{
		Name:        "test-snap",
		Description: "test snapshot",
	})
	if snapResp.StatusCode != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", snapResp.StatusCode)
	}
	snapResp.Body.Close()

	// List snapshots
	listResp := doJSON(t, ts, http.MethodGet, "/sandboxes/"+created.SandboxID+"/snapshots", nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", listResp.StatusCode)
	}
	listResp.Body.Close()
}

// ----- E2E Test: Access Token -----

func TestE2E_AccessToken(t *testing.T) {
	ts := testServer(t)

	createResp := doJSON(t, ts, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	var created dto.SandboxCreateResponse
	decodeJSON(t, createResp, &created)

	tokenResp := doJSON(t, ts, http.MethodPost, "/sandboxes/"+created.SandboxID+"/access-token", nil)
	if tokenResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", tokenResp.StatusCode)
	}

	var result dto.AccessTokenResponse
	decodeJSON(t, tokenResp, &result)
	if result.AccessToken == "" {
		t.Error("expected non-empty access token")
	}
}

// ----- E2E Test: Ports -----

func TestE2E_Ports(t *testing.T) {
	ts := testServer(t)

	createResp := doJSON(t, ts, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	var created dto.SandboxCreateResponse
	decodeJSON(t, createResp, &created)

	portsResp := doJSON(t, ts, http.MethodGet, "/sandboxes/"+created.SandboxID+"/ports", nil)
	if portsResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", portsResp.StatusCode)
	}
	portsResp.Body.Close()
}

// ----- E2E Test: Processes -----

func TestE2E_Processes(t *testing.T) {
	ts := testServer(t)

	createResp := doJSON(t, ts, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	var created dto.SandboxCreateResponse
	decodeJSON(t, createResp, &created)

	procsResp := doJSON(t, ts, http.MethodGet, "/sandboxes/"+created.SandboxID+"/processes", nil)
	if procsResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", procsResp.StatusCode)
	}
	procsResp.Body.Close()
}

// ----- E2E Test: Commands (envd-compatible) -----

func TestE2E_Commands_EnvdCompatible(t *testing.T) {
	ts := testServer(t)

	createResp := doJSON(t, ts, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	var created dto.SandboxCreateResponse
	decodeJSON(t, createResp, &created)

	// POST /commands (envd-style)
	cmdResp := doJSON(t, ts, http.MethodPost, "/sandboxes/"+created.SandboxID+"/commands", dto.CommandRequest{
		Command: "echo hello",
	})
	if cmdResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", cmdResp.StatusCode)
	}
	cmdResp.Body.Close()

	// GET /commands (returns process list in envd style)
	listResp := doJSON(t, ts, http.MethodGet, "/sandboxes/"+created.SandboxID+"/commands", nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list commands: expected 200, got %d", listResp.StatusCode)
	}
	listResp.Body.Close()
}

// ----- E2E Test: Health and Ready -----

func TestE2E_HealthAndReady(t *testing.T) {
	ts := testServer(t)

	healthResp := doJSON(t, ts, http.MethodGet, "/healthz", nil)
	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: expected 200, got %d", healthResp.StatusCode)
	}
	healthResp.Body.Close()

	readyResp := doJSON(t, ts, http.MethodGet, "/readyz", nil)
	if readyResp.StatusCode != http.StatusOK {
		t.Fatalf("readyz: expected 200, got %d", readyResp.StatusCode)
	}
	readyResp.Body.Close()
}

// ----- E2E Test: Template Build and Alias -----

func TestE2E_TemplateBuildAndAlias(t *testing.T) {
	ts := testServer(t)

	// Create template
	createResp := doJSON(t, ts, http.MethodPost, "/templates", dto.TemplateBuildRequest{
		Name:     "build-test",
		CPUCount: 1,
		MemoryMB: 256,
	})
	var build dto.TemplateBuildResponse
	decodeJSON(t, createResp, &build)

	// Get build status
	statusResp := doJSON(t, ts, http.MethodPost, "/templates/"+build.TemplateID+"/builds/"+build.BuildID+"/status", nil)
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("build status: expected 200, got %d", statusResp.StatusCode)
	}
	statusResp.Body.Close()

	// Create alias
	aliasResp := doJSON(t, ts, http.MethodPost, "/templates/"+build.TemplateID+"/aliases", dto.AliasRequest{
		Alias: "latest",
	})
	if aliasResp.StatusCode != http.StatusCreated {
		t.Fatalf("create alias: expected 201, got %d", aliasResp.StatusCode)
	}
	aliasResp.Body.Close()

	// Delete alias
	delAliasResp := doJSON(t, ts, http.MethodDelete, "/templates/"+build.TemplateID+"/aliases/latest", nil)
	if delAliasResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete alias: expected 204, got %d", delAliasResp.StatusCode)
	}
	delAliasResp.Body.Close()
}

// ----- E2E Test: v2 Create Template -----

func TestE2E_V2_CreateTemplate(t *testing.T) {
	ts := testServer(t)

	resp := doJSON(t, ts, http.MethodPost, "/v2/templates", dto.V2TemplateCreateRequest{
		Name:     "v2-template",
		CPUCount: 2,
		MemoryMB: 512,
		Tags:     []string{"latest"},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// Ensure unused imports are used
var _ = strings.NewReader
