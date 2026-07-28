package v1_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	mockadapter "github.com/e2bgateway/e2bgateway/internal/adapter/mock"
	"github.com/e2bgateway/e2bgateway/internal/api/dto"
	v1 "github.com/e2bgateway/e2bgateway/internal/api/v1"
	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/routing"
)

func setupTestRouter() (*adapter.Registry, *routing.Router) {
	reg := adapter.NewRegistry()
	_ = reg.Register(mockadapter.New())

	r := routing.NewRouter(config.RoutingConfig{
		DefaultBackend: "mock",
		Strategy:       "static",
	}, reg)

	return reg, r
}

// testWithChiParam creates a request with chi URL params set.
func testWithChiParam(method, path string, body string, params map[string]string) *http.Request {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Content-Type", "application/json")

	// Set up chi route context
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	return req
}

func TestHealthHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	v1.HealthHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Errorf("expected status 'ok', got %s", resp["status"])
	}
}

func TestReadyHandler(t *testing.T) {
	reg, _ := setupTestRouter()
	handler := v1.ReadyHandler(reg)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestCreateSandboxHandler(t *testing.T) {
	reg, r := setupTestRouter()
	handler := v1.CreateSandboxHandler(reg, r, "")

	body := `{"templateID": "base", "timeout": 300}`
	req := httptest.NewRequest(http.MethodPost, "/sandboxes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp dto.SandboxCreateResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.SandboxID == "" {
		t.Error("expected non-empty sandbox ID")
	}
	if resp.TemplateID != "base" {
		t.Errorf("expected template 'base', got %s", resp.TemplateID)
	}
	if resp.EnvdAccessToken == "" {
		t.Error("expected non-empty envdAccessToken")
	}
	if resp.EnvdVersion == "" {
		t.Error("expected non-empty envdVersion")
	}
}

func TestCreateSandboxHandlerInvalidBody(t *testing.T) {
	reg, r := setupTestRouter()
	handler := v1.CreateSandboxHandler(reg, r, "")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", strings.NewReader("invalid json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

func TestListSandboxesHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Create a sandbox first
	createHandler := v1.CreateSandboxHandler(reg, r, "")
	body := `{"templateID": "base"}`
	req := httptest.NewRequest(http.MethodPost, "/sandboxes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createHandler(w, req)

	// List sandboxes
	listHandler := v1.ListSandboxesHandler(reg, r)
	req = httptest.NewRequest(http.MethodGet, "/sandboxes", nil)
	w = httptest.NewRecorder()
	listHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var sandboxes []dto.SandboxInfo
	_ = json.NewDecoder(w.Body).Decode(&sandboxes)
	if len(sandboxes) < 1 {
		t.Error("expected at least 1 sandbox")
	}
	// Verify E2B format: state field instead of status
	if sandboxes[0].State == "" {
		t.Error("expected non-empty state field (E2B format)")
	}
}

func TestListTemplatesHandler(t *testing.T) {
	reg, r := setupTestRouter()
	handler := v1.ListTemplatesHandler(reg, r)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/templates", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var templates []*dto.TemplateInfo
	_ = json.NewDecoder(w.Body).Decode(&templates)
	if len(templates) < 1 {
		t.Error("expected at least 1 template")
	}
}

func TestExecuteCodeHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Create a sandbox first
	createHandler := v1.CreateSandboxHandler(reg, r, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", strings.NewReader(`{"templateID": "base"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createHandler(w, req)

	var sbx dto.SandboxCreateResponse
	_ = json.NewDecoder(w.Body).Decode(&sbx)

	// Execute code
	execHandler := v1.ExecuteCodeHandler(reg, r)
	body := `{"code": "print('hello')", "language": "python"}`
	execReq := testWithChiParam(http.MethodPost, "/api/v1/sandboxes/"+sbx.SandboxID+"/code", body, map[string]string{
		"sandboxID": sbx.SandboxID,
	})
	w = httptest.NewRecorder()
	execHandler(w, execReq)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestRunCommandHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Create a sandbox
	createHandler := v1.CreateSandboxHandler(reg, r, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", strings.NewReader(`{"templateID": "base"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createHandler(w, req)

	var sbx dto.SandboxCreateResponse
	_ = json.NewDecoder(w.Body).Decode(&sbx)

	// Run command
	cmdHandler := v1.RunCommandHandler(reg, r)
	body := `{"command": "echo hello"}`
	cmdReq := testWithChiParam(http.MethodPost, "/api/v1/sandboxes/"+sbx.SandboxID+"/commands", body, map[string]string{
		"sandboxID": sbx.SandboxID,
	})
	w = httptest.NewRecorder()
	cmdHandler(w, cmdReq)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestWriteFileHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Create a sandbox
	createHandler := v1.CreateSandboxHandler(reg, r, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", strings.NewReader(`{"templateID": "base"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createHandler(w, req)

	var sbx dto.SandboxCreateResponse
	_ = json.NewDecoder(w.Body).Decode(&sbx)

	// Write file
	writeHandler := v1.WriteFileHandler(reg, r)
	body := `{"path": "/test.txt", "content": "aGVsbG8gd29ybGQ="}` // base64 encoded "hello world"
	writeReq := testWithChiParam(http.MethodPost, "/api/v1/sandboxes/"+sbx.SandboxID+"/files/write", body, map[string]string{
		"sandboxID": sbx.SandboxID,
	})
	w = httptest.NewRecorder()
	writeHandler(w, writeReq)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected status 204, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestReadFileHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Create a sandbox
	createHandler := v1.CreateSandboxHandler(reg, r, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", strings.NewReader(`{"templateID": "base"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createHandler(w, req)

	var sbx dto.SandboxCreateResponse
	_ = json.NewDecoder(w.Body).Decode(&sbx)

	// Read file
	readHandler := v1.ReadFileHandler(reg, r)
	readReq := testWithChiParam(http.MethodGet, "/api/v1/sandboxes/"+sbx.SandboxID+"/files/read?path=/test.txt", "", map[string]string{
		"sandboxID": sbx.SandboxID,
	})
	w = httptest.NewRecorder()
	readHandler(w, readReq)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestReadFileHandlerMissingPath(t *testing.T) {
	reg, r := setupTestRouter()
	handler := v1.ReadFileHandler(reg, r)

	req := testWithChiParam(http.MethodGet, "/api/v1/sandboxes/test-sbx/files/read", "", map[string]string{
		"sandboxID": "test-sbx",
	})
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

func TestListFilesHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Create a sandbox
	createHandler := v1.CreateSandboxHandler(reg, r, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", strings.NewReader(`{"templateID": "base"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createHandler(w, req)

	var sbx dto.SandboxCreateResponse
	_ = json.NewDecoder(w.Body).Decode(&sbx)

	// List files
	listHandler := v1.ListFilesHandler(reg, r)
	listReq := testWithChiParam(http.MethodGet, "/api/v1/sandboxes/"+sbx.SandboxID+"/files/list?path=/", "", map[string]string{
		"sandboxID": sbx.SandboxID,
	})
	w = httptest.NewRecorder()
	listHandler(w, listReq)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestMakeDirHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Create a sandbox
	createHandler := v1.CreateSandboxHandler(reg, r, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", strings.NewReader(`{"templateID": "base"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createHandler(w, req)

	var sbx dto.SandboxCreateResponse
	_ = json.NewDecoder(w.Body).Decode(&sbx)

	// Make dir
	mkdirHandler := v1.MakeDirHandler(reg, r)
	body := `{"path": "/test-dir"}`
	mkdirReq := testWithChiParam(http.MethodPost, "/api/v1/sandboxes/"+sbx.SandboxID+"/files/make-dir", body, map[string]string{
		"sandboxID": sbx.SandboxID,
	})
	w = httptest.NewRecorder()
	mkdirHandler(w, mkdirReq)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected status 204, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestRemoveFileHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Create a sandbox
	createHandler := v1.CreateSandboxHandler(reg, r, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", strings.NewReader(`{"templateID": "base"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createHandler(w, req)

	var sbx dto.SandboxCreateResponse
	_ = json.NewDecoder(w.Body).Decode(&sbx)

	// Remove file
	removeHandler := v1.RemoveFileHandler(reg, r)
	body := `{"path": "/test.txt"}`
	removeReq := testWithChiParam(http.MethodPost, "/api/v1/sandboxes/"+sbx.SandboxID+"/files/remove", body, map[string]string{
		"sandboxID": sbx.SandboxID,
	})
	w = httptest.NewRecorder()
	removeHandler(w, removeReq)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected status 204, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestSetTimeoutHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Create a sandbox
	createHandler := v1.CreateSandboxHandler(reg, r, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", strings.NewReader(`{"templateID": "base"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createHandler(w, req)

	var sbx dto.SandboxCreateResponse
	_ = json.NewDecoder(w.Body).Decode(&sbx)

	// Set timeout
	timeoutHandler := v1.SetTimeoutHandler(reg, r)
	body := `{"timeout": 600}`
	timeoutReq := testWithChiParam(http.MethodPatch, "/api/v1/sandboxes/"+sbx.SandboxID+"/timeout", body, map[string]string{
		"sandboxID": sbx.SandboxID,
	})
	w = httptest.NewRecorder()
	timeoutHandler(w, timeoutReq)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected status 204, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestPauseResumeSandboxHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Create a sandbox
	createHandler := v1.CreateSandboxHandler(reg, r, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", strings.NewReader(`{"templateID": "base"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createHandler(w, req)

	var sbx dto.SandboxCreateResponse
	_ = json.NewDecoder(w.Body).Decode(&sbx)

	// Pause sandbox
	pauseHandler := v1.PauseSandboxHandler(reg, r)
	pauseReq := testWithChiParam(http.MethodPost, "/api/v1/sandboxes/"+sbx.SandboxID+"/pause", "", map[string]string{
		"sandboxID": sbx.SandboxID,
	})
	w = httptest.NewRecorder()
	pauseHandler(w, pauseReq)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected status 204, got %d; body: %s", w.Code, w.Body.String())
	}

	// Resume sandbox
	resumeHandler := v1.ResumeSandboxHandler(reg, r)
	resumeReq := testWithChiParam(http.MethodPost, "/api/v1/sandboxes/"+sbx.SandboxID+"/resume", "", map[string]string{
		"sandboxID": sbx.SandboxID,
	})
	w = httptest.NewRecorder()
	resumeHandler(w, resumeReq)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestKillSandboxHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Create a sandbox
	createHandler := v1.CreateSandboxHandler(reg, r, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", strings.NewReader(`{"templateID": "base"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createHandler(w, req)

	var sbx dto.SandboxCreateResponse
	_ = json.NewDecoder(w.Body).Decode(&sbx)

	// Kill sandbox
	killHandler := v1.KillSandboxHandler(reg, r)
	killReq := testWithChiParam(http.MethodDelete, "/api/v1/sandboxes/"+sbx.SandboxID, "", map[string]string{
		"sandboxID": sbx.SandboxID,
	})
	w = httptest.NewRecorder()
	killHandler(w, killReq)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected status 204, got %d; body: %s", w.Code, w.Body.String())
	}
}

// --- New Handler Tests ---

func TestCreateTemplateHandler(t *testing.T) {
	reg, r := setupTestRouter()
	handler := v1.CreateTemplateHandler(reg, r)

	body := `{"name": "my-template", "cpuCount": 4, "memoryMB": 2048}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/templates", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected status 202, got %d; body: %s", w.Code, w.Body.String())
	}

	var build dto.TemplateBuildResponse
	_ = json.NewDecoder(w.Body).Decode(&build)
	if build.TemplateID == "" {
		t.Error("expected non-empty template ID")
	}
}

func TestDeleteTemplateHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Create a template first
	createHandler := v1.CreateTemplateHandler(reg, r)
	body := `{"name": "to-delete"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/templates", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createHandler(w, req)

	var build dto.TemplateBuildResponse
	_ = json.NewDecoder(w.Body).Decode(&build)

	// Delete template
	deleteHandler := v1.DeleteTemplateHandler(reg, r)
	deleteReq := testWithChiParam(http.MethodDelete, "/api/v1/templates/"+build.TemplateID, "", map[string]string{
		"templateID": build.TemplateID,
	})
	w = httptest.NewRecorder()
	deleteHandler(w, deleteReq)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected status 204, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestListWarmPoolsHandler(t *testing.T) {
	reg, r := setupTestRouter()
	handler := v1.ListWarmPoolsHandler(reg, r)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/warm-pools", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestCreateWarmPoolHandler(t *testing.T) {
	reg, r := setupTestRouter()
	handler := v1.CreateWarmPoolHandler(reg, r)

	body := `{"templateID": "base", "targetSize": 3}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/warm-pools", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestWarmPoolLifecycle(t *testing.T) {
	reg, r := setupTestRouter()

	// Create warm pool
	createHandler := v1.CreateWarmPoolHandler(reg, r)
	body := `{"templateID": "base", "targetSize": 2}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/warm-pools", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createHandler(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", w.Code)
	}

	var pool dto.WarmPoolInfo
	_ = json.NewDecoder(w.Body).Decode(&pool)

	// Get warm pool
	getHandler := v1.GetWarmPoolHandler(reg, r)
	getReq := testWithChiParam(http.MethodGet, "/api/v1/warm-pools/"+pool.WarmPoolID, "", map[string]string{
		"warmPoolID": pool.WarmPoolID,
	})
	w = httptest.NewRecorder()
	getHandler(w, getReq)

	if w.Code != http.StatusOK {
		t.Errorf("get: expected 200, got %d", w.Code)
	}

	// Update size
	updateHandler := v1.UpdateWarmPoolSizeHandler(reg, r)
	updateReq := testWithChiParam(http.MethodPost, "/api/v1/warm-pools/"+pool.WarmPoolID+"/size", `{"targetSize": 5}`, map[string]string{
		"warmPoolID": pool.WarmPoolID,
	})
	w = httptest.NewRecorder()
	updateHandler(w, updateReq)

	if w.Code != http.StatusNoContent {
		t.Errorf("update: expected 204, got %d", w.Code)
	}

	// Delete warm pool
	deleteHandler := v1.DeleteWarmPoolHandler(reg, r)
	deleteReq := testWithChiParam(http.MethodDelete, "/api/v1/warm-pools/"+pool.WarmPoolID, "", map[string]string{
		"warmPoolID": pool.WarmPoolID,
	})
	w = httptest.NewRecorder()
	deleteHandler(w, deleteReq)

	if w.Code != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d", w.Code)
	}
}

func TestListProcessesHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Create a sandbox
	createHandler := v1.CreateSandboxHandler(reg, r, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", strings.NewReader(`{"templateID": "base"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createHandler(w, req)

	var sbx dto.SandboxCreateResponse
	_ = json.NewDecoder(w.Body).Decode(&sbx)

	// List processes
	listHandler := v1.ListProcessesHandler(reg, r)
	listReq := testWithChiParam(http.MethodGet, "/api/v1/sandboxes/"+sbx.SandboxID+"/processes", "", map[string]string{
		"sandboxID": sbx.SandboxID,
	})
	w = httptest.NewRecorder()
	listHandler(w, listReq)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestSnapshotLifecycle(t *testing.T) {
	reg, r := setupTestRouter()

	// Create a sandbox
	createHandler := v1.CreateSandboxHandler(reg, r, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", strings.NewReader(`{"templateID": "base"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createHandler(w, req)

	var sbx dto.SandboxCreateResponse
	_ = json.NewDecoder(w.Body).Decode(&sbx)

	// Create snapshot
	snapHandler := v1.CreateSnapshotHandler(reg, r)
	body := `{"name": "my-snapshot", "description": "test"}`
	snapReq := testWithChiParam(http.MethodPost, "/api/v1/sandboxes/"+sbx.SandboxID+"/snapshots", body, map[string]string{
		"sandboxID": sbx.SandboxID,
	})
	w = httptest.NewRecorder()
	snapHandler(w, snapReq)

	if w.Code != http.StatusCreated {
		t.Errorf("create: expected 201, got %d; body: %s", w.Code, w.Body.String())
	}

	// List snapshots
	listHandler := v1.ListSnapshotsHandler(reg, r)
	listReq := testWithChiParam(http.MethodGet, "/api/v1/sandboxes/"+sbx.SandboxID+"/snapshots", "", map[string]string{
		"sandboxID": sbx.SandboxID,
	})
	w = httptest.NewRecorder()
	listHandler(w, listReq)

	if w.Code != http.StatusOK {
		t.Errorf("list: expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestListPortsHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Create a sandbox
	createHandler := v1.CreateSandboxHandler(reg, r, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", strings.NewReader(`{"templateID": "base"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createHandler(w, req)

	var sbx dto.SandboxCreateResponse
	_ = json.NewDecoder(w.Body).Decode(&sbx)

	// List ports
	listHandler := v1.ListPortsHandler(reg, r)
	listReq := testWithChiParam(http.MethodGet, "/api/v1/sandboxes/"+sbx.SandboxID+"/ports", "", map[string]string{
		"sandboxID": sbx.SandboxID,
	})
	w = httptest.NewRecorder()
	listHandler(w, listReq)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestGetPortURLHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Create a sandbox
	createHandler := v1.CreateSandboxHandler(reg, r, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", strings.NewReader(`{"templateID": "base"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createHandler(w, req)

	var sbx dto.SandboxCreateResponse
	_ = json.NewDecoder(w.Body).Decode(&sbx)

	// Get port URL
	portHandler := v1.GetPortURLHandler(reg, r)
	portReq := testWithChiParam(http.MethodGet, "/api/v1/sandboxes/"+sbx.SandboxID+"/ports/3000", "", map[string]string{
		"sandboxID": sbx.SandboxID,
		"port":      "3000",
	})
	w = httptest.NewRecorder()
	portHandler(w, portReq)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestGetAccessTokenHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Create a sandbox
	createHandler := v1.CreateSandboxHandler(reg, r, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", strings.NewReader(`{"templateID": "base"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createHandler(w, req)

	var sbx dto.SandboxCreateResponse
	_ = json.NewDecoder(w.Body).Decode(&sbx)

	// Get access token
	tokenHandler := v1.GetAccessTokenHandler(reg, r)
	tokenReq := testWithChiParam(http.MethodPost, "/api/v1/sandboxes/"+sbx.SandboxID+"/access-token", "", map[string]string{
		"sandboxID": sbx.SandboxID,
	})
	w = httptest.NewRecorder()
	tokenHandler(w, tokenReq)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["accessToken"] == nil || resp["accessToken"] == "" {
		t.Error("expected non-empty access token")
	}
}

func TestTriggerBuildHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Trigger build
	buildHandler := v1.TriggerBuildHandler(reg, r)
	body := `{"dockerfile": "FROM python:3.11"}`
	req := testWithChiParam(http.MethodPost, "/api/v1/templates/base/builds", body, map[string]string{
		"templateID": "base",
	})
	w := httptest.NewRecorder()
	buildHandler(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected status 202, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestGetBuildStatusHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Trigger a build first
	buildHandler := v1.TriggerBuildHandler(reg, r)
	body := `{"dockerfile": "FROM python:3.11"}`
	req := testWithChiParam(http.MethodPost, "/api/v1/templates/base/builds", body, map[string]string{
		"templateID": "base",
	})
	w := httptest.NewRecorder()
	buildHandler(w, req)

	var build dto.TemplateBuildResponse
	_ = json.NewDecoder(w.Body).Decode(&build)

	// Get build status
	statusHandler := v1.GetBuildStatusHandler(reg, r)
	statusReq := testWithChiParam(http.MethodPost, "/api/v1/templates/base/builds/"+build.BuildID+"/status", "", map[string]string{
		"templateID": "base",
		"buildID":    build.BuildID,
	})
	w = httptest.NewRecorder()
	statusHandler(w, statusReq)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestCreateAliasHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Create alias
	aliasHandler := v1.CreateAliasHandler(reg, r)
	body := `{"alias": "latest"}`
	req := testWithChiParam(http.MethodPost, "/api/v1/templates/base/aliases", body, map[string]string{
		"templateID": "base",
	})
	w := httptest.NewRecorder()
	aliasHandler(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestDeleteAliasHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Create alias first
	createHandler := v1.CreateAliasHandler(reg, r)
	body := `{"alias": "v1"}`
	req := testWithChiParam(http.MethodPost, "/api/v1/templates/base/aliases", body, map[string]string{
		"templateID": "base",
	})
	w := httptest.NewRecorder()
	createHandler(w, req)

	// Delete alias
	deleteHandler := v1.DeleteAliasHandler(reg, r)
	deleteReq := testWithChiParam(http.MethodDelete, "/api/v1/templates/base/aliases/v1", "", map[string]string{
		"templateID": "base",
		"alias":      "v1",
	})
	w = httptest.NewRecorder()
	deleteHandler(w, deleteReq)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected status 204, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestStartExecutionHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Create a sandbox
	createHandler := v1.CreateSandboxHandler(reg, r, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", strings.NewReader(`{"templateID": "base"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createHandler(w, req)

	var sbx dto.SandboxCreateResponse
	_ = json.NewDecoder(w.Body).Decode(&sbx)

	// Start execution
	execHandler := v1.StartExecutionHandler(reg, r)
	body := `{"code": "print('hello')", "language": "python"}`
	execReq := testWithChiParam(http.MethodPost, "/api/v1/sandboxes/"+sbx.SandboxID+"/code/executions", body, map[string]string{
		"sandboxID": sbx.SandboxID,
	})
	w = httptest.NewRecorder()
	execHandler(w, execReq)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected status 202, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestGetExecutionHandler(t *testing.T) {
	reg, r := setupTestRouter()
	handler := v1.GetExecutionHandler(reg, r)

	req := testWithChiParam(http.MethodGet, "/api/v1/sandboxes/test-sbx/code/executions/exec-123", "", map[string]string{
		"sandboxID":   "test-sbx",
		"executionID": "exec-123",
	})
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Errorf("expected status 501, got %d", w.Code)
	}
}

func TestUploadFileHandler(t *testing.T) {
	reg, r := setupTestRouter()

	// Create a sandbox
	createHandler := v1.CreateSandboxHandler(reg, r, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", strings.NewReader(`{"templateID": "base"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createHandler(w, req)

	var sbx dto.SandboxCreateResponse
	_ = json.NewDecoder(w.Body).Decode(&sbx)

	// Upload file via multipart form
	uploadHandler := v1.UploadFileHandler(reg, r)

	// Create multipart body
	var buf bytes.Buffer
	buf.WriteString("--boundary\r\n")
	buf.WriteString("Content-Disposition: form-data; name=\"path\"\r\n\r\n")
	buf.WriteString("/uploaded.txt\r\n")
	buf.WriteString("--boundary\r\n")
	buf.WriteString("Content-Disposition: form-data; name=\"file\"; filename=\"test.txt\"\r\n")
	buf.WriteString("Content-Type: text/plain\r\n\r\n")
	buf.WriteString("file content here\r\n")
	buf.WriteString("--boundary--\r\n")

	uploadReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sbx.SandboxID+"/files/upload", &buf)
	uploadReq.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")

	// Set chi context
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sandboxID", sbx.SandboxID)
	uploadReq = uploadReq.WithContext(context.WithValue(uploadReq.Context(), chi.RouteCtxKey, rctx))

	w = httptest.NewRecorder()
	uploadHandler(w, uploadReq)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected status 204, got %d; body: %s", w.Code, w.Body.String())
	}
}
