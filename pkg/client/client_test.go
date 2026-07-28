package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// -----------------------------------------------------------------------
// Test helpers
// -----------------------------------------------------------------------

// testServer creates an httptest.Server that dispatches to a handler map
// keyed by "METHOD /path". Paths may contain a trailing wildcard via the
// prefix helper (see testServerPrefix).
func testServer(t *testing.T, handlers map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for pattern, h := range handlers {
		mux.HandleFunc(pattern, h)
	}
	return httptest.NewServer(mux)
}

// writeJSON helper for tests.
func writeJSON(t *testing.T, w http.ResponseWriter, status int, v interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}
}

// writeError helper for tests (matches the server's error format).
func writeErr(t *testing.T, w http.ResponseWriter, status int, msg string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":    status,
		"message": msg,
	})
}

// -----------------------------------------------------------------------
// Construction & options
// -----------------------------------------------------------------------

func TestNew(t *testing.T) {
	c := New("http://localhost:8080/")
	if c.baseURL != "http://localhost:8080" {
		t.Errorf("trailing slash not stripped: got %q", c.baseURL)
	}
	if c.apiKey != "" {
		t.Errorf("apiKey should default to empty")
	}
	if c.httpClient == nil {
		t.Fatal("httpClient should not be nil")
	}
}

func TestWithAPIKey(t *testing.T) {
	var gotKey string
	ts := testServer(t, map[string]http.HandlerFunc{
		"/healthz": func(w http.ResponseWriter, r *http.Request) {
			gotKey = r.Header.Get("X-API-Key")
			writeJSON(t, w, http.StatusOK, HealthStatus{Status: "ok"})
		},
	})
	defer ts.Close()

	c := New(ts.URL, WithAPIKey("secret-key"))
	if _, err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if gotKey != "secret-key" {
		t.Errorf("X-API-Key = %q, want %q", gotKey, "secret-key")
	}
}

func TestWithHTTPClient(t *testing.T) {
	custom := &http.Client{Timeout: 5 * time.Second}
	c := New("http://localhost", WithHTTPClient(custom))
	if c.httpClient != custom {
		t.Error("WithHTTPClient did not install the custom client")
	}
}

func TestWithHTTPClientNilIsIgnored(t *testing.T) {
	c := New("http://localhost", WithHTTPClient(nil))
	if c.httpClient == nil {
		t.Fatal("nil http client should not replace the default")
	}
}

func TestWithTimeout(t *testing.T) {
	c := New("http://localhost", WithTimeout(42*time.Second))
	if c.httpClient.Timeout != 42*time.Second {
		t.Errorf("timeout = %v, want 42s", c.httpClient.Timeout)
	}
}

// -----------------------------------------------------------------------
// Health
// -----------------------------------------------------------------------

func TestHealth(t *testing.T) {
	ts := testServer(t, map[string]http.HandlerFunc{
		"/healthz": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				writeErr(t, w, http.StatusMethodNotAllowed, "bad method")
				return
			}
			writeJSON(t, w, http.StatusOK, map[string]string{"status": "ok"})
		},
	})
	defer ts.Close()

	c := New(ts.URL)
	hs, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if hs.Status != "ok" {
		t.Errorf("status = %q, want %q", hs.Status, "ok")
	}
}

func TestHealth_ServerError(t *testing.T) {
	ts := testServer(t, map[string]http.HandlerFunc{
		"/healthz": func(w http.ResponseWriter, _ *http.Request) {
			writeErr(t, w, http.StatusInternalServerError, "boom")
		},
	})
	defer ts.Close()

	c := New(ts.URL)
	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", apiErr.StatusCode)
	}
	if apiErr.Message != "boom" {
		t.Errorf("Message = %q, want %q", apiErr.Message, "boom")
	}
}

// -----------------------------------------------------------------------
// Sandboxes
// -----------------------------------------------------------------------

func TestCreateSandbox(t *testing.T) {
	ts := testServer(t, map[string]http.HandlerFunc{
		"/sandboxes": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				writeErr(t, w, http.StatusMethodNotAllowed, "bad method")
				return
			}
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				writeErr(t, w, http.StatusBadRequest, "bad content-type")
				return
			}

			var req CreateSandboxRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeErr(t, w, http.StatusBadRequest, err.Error())
				return
			}
			if req.TemplateID != "base" {
				writeErr(t, w, http.StatusBadRequest, "wrong templateID")
				return
			}

			writeJSON(t, w, http.StatusCreated, SandboxCreateResponse{
				SandboxID:  "sb-123",
				TemplateID: req.TemplateID,
				Alias:      req.Alias,
			})
		},
	})
	defer ts.Close()

	c := New(ts.URL)
	resp, err := c.CreateSandbox(context.Background(), CreateSandboxRequest{
		TemplateID: "base",
		Alias:      "my-alias",
		Timeout:    300,
		Metadata:   map[string]string{"env": "test"},
	})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	if resp.SandboxID != "sb-123" {
		t.Errorf("SandboxID = %q, want %q", resp.SandboxID, "sb-123")
	}
	if resp.TemplateID != "base" {
		t.Errorf("TemplateID = %q, want %q", resp.TemplateID, "base")
	}
	if resp.Alias != "my-alias" {
		t.Errorf("Alias = %q, want %q", resp.Alias, "my-alias")
	}
}

func TestListSandboxes(t *testing.T) {
	ts := testServer(t, map[string]http.HandlerFunc{
		"/sandboxes": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				writeErr(t, w, http.StatusMethodNotAllowed, "bad method")
				return
			}
			writeJSON(t, w, http.StatusOK, []SandboxInfo{
				{SandboxID: "sb-1", TemplateID: "t1", State: "running"},
				{SandboxID: "sb-2", TemplateID: "t2", State: "paused"},
			})
		},
	})
	defer ts.Close()

	c := New(ts.URL)
	sbs, err := c.ListSandboxes(context.Background())
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if len(sbs) != 2 {
		t.Fatalf("len = %d, want 2", len(sbs))
	}
	if sbs[0].SandboxID != "sb-1" || sbs[1].SandboxID != "sb-2" {
		t.Errorf("unexpected sandbox IDs: %+v", sbs)
	}
}

func TestListSandboxes_EmptyArray(t *testing.T) {
	ts := testServer(t, map[string]http.HandlerFunc{
		"/sandboxes": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusOK, []SandboxInfo{})
		},
	})
	defer ts.Close()

	c := New(ts.URL)
	sbs, err := c.ListSandboxes(context.Background())
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if len(sbs) != 0 {
		t.Errorf("len = %d, want 0", len(sbs))
	}
}

func TestListSandboxes_NullArray(t *testing.T) {
	// Server may return "null" instead of "[]". Client should normalise to [].
	ts := testServer(t, map[string]http.HandlerFunc{
		"/sandboxes": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "null")
		},
	})
	defer ts.Close()

	c := New(ts.URL)
	sbs, err := c.ListSandboxes(context.Background())
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if sbs == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(sbs) != 0 {
		t.Errorf("len = %d, want 0", len(sbs))
	}
}

func TestGetSandbox(t *testing.T) {
	ts := testServer(t, map[string]http.HandlerFunc{
		"/sandboxes/sb-123": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				writeErr(t, w, http.StatusMethodNotAllowed, "bad method")
				return
			}
			writeJSON(t, w, http.StatusOK, SandboxInfo{
				SandboxID:  "sb-123",
				TemplateID: "base",
				State:      "running",
			})
		},
	})
	defer ts.Close()

	c := New(ts.URL)
	sb, err := c.GetSandbox(context.Background(), "sb-123")
	if err != nil {
		t.Fatalf("GetSandbox: %v", err)
	}
	if sb.SandboxID != "sb-123" {
		t.Errorf("SandboxID = %q, want %q", sb.SandboxID, "sb-123")
	}
}

func TestGetSandbox_NotFound(t *testing.T) {
	ts := testServer(t, map[string]http.HandlerFunc{
		"/sandboxes/missing": func(w http.ResponseWriter, _ *http.Request) {
			writeErr(t, w, http.StatusNotFound, "Sandbox 'missing' not found")
		},
	})
	defer ts.Close()

	c := New(ts.URL)
	_, err := c.GetSandbox(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want 404", apiErr.StatusCode)
	}
}

func TestKillSandbox(t *testing.T) {
	var called bool
	ts := testServer(t, map[string]http.HandlerFunc{
		"/sandboxes/sb-123": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete {
				writeErr(t, w, http.StatusMethodNotAllowed, "bad method")
				return
			}
			called = true
			w.WriteHeader(http.StatusNoContent)
		},
	})
	defer ts.Close()

	c := New(ts.URL)
	if err := c.KillSandbox(context.Background(), "sb-123"); err != nil {
		t.Fatalf("KillSandbox: %v", err)
	}
	if !called {
		t.Error("handler not called")
	}
}

func TestPauseSandbox(t *testing.T) {
	var called bool
	ts := testServer(t, map[string]http.HandlerFunc{
		"/sandboxes/sb-1/pause": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				writeErr(t, w, http.StatusMethodNotAllowed, "bad method")
				return
			}
			called = true
			w.WriteHeader(http.StatusNoContent)
		},
	})
	defer ts.Close()

	c := New(ts.URL)
	if err := c.PauseSandbox(context.Background(), "sb-1"); err != nil {
		t.Fatalf("PauseSandbox: %v", err)
	}
	if !called {
		t.Error("handler not called")
	}
}

func TestResumeSandbox(t *testing.T) {
	ts := testServer(t, map[string]http.HandlerFunc{
		"/sandboxes/sb-1/resume": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				writeErr(t, w, http.StatusMethodNotAllowed, "bad method")
				return
			}
			writeJSON(t, w, http.StatusOK, SandboxInfo{
				SandboxID: "sb-1",
				State:     "running",
			})
		},
	})
	defer ts.Close()

	c := New(ts.URL)
	sb, err := c.ResumeSandbox(context.Background(), "sb-1")
	if err != nil {
		t.Fatalf("ResumeSandbox: %v", err)
	}
	if sb.State != "running" {
		t.Errorf("State = %q, want %q", sb.State, "running")
	}
}

func TestSetTimeout(t *testing.T) {
	var gotBody string
	ts := testServer(t, map[string]http.HandlerFunc{
		"/sandboxes/sb-1/timeout": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPatch {
				writeErr(t, w, http.StatusMethodNotAllowed, "bad method")
				return
			}
			data, _ := io.ReadAll(r.Body)
			gotBody = string(data)
			w.WriteHeader(http.StatusNoContent)
		},
	})
	defer ts.Close()

	c := New(ts.URL)
	if err := c.SetTimeout(context.Background(), "sb-1", 600); err != nil {
		t.Fatalf("SetTimeout: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(gotBody), &parsed); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if int(parsed["timeout"].(float64)) != 600 {
		t.Errorf("timeout = %v, want 600", parsed["timeout"])
	}
}

// -----------------------------------------------------------------------
// Templates
// -----------------------------------------------------------------------

func TestListTemplates(t *testing.T) {
	ts := testServer(t, map[string]http.HandlerFunc{
		"/templates": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				writeErr(t, w, http.StatusMethodNotAllowed, "bad method")
				return
			}
			writeJSON(t, w, http.StatusOK, []TemplateInfo{
				{TemplateID: "t1", CPUCount: 2, MemoryMB: 512, Public: false, Ready: true},
				{TemplateID: "t2", CPUCount: 4, MemoryMB: 1024, Public: true, Ready: true},
			})
		},
	})
	defer ts.Close()

	c := New(ts.URL)
	tpls, err := c.ListTemplates(context.Background())
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(tpls) != 2 {
		t.Fatalf("len = %d, want 2", len(tpls))
	}
	if tpls[0].TemplateID != "t1" || tpls[1].TemplateID != "t2" {
		t.Errorf("unexpected template IDs: %+v", tpls)
	}
}

func TestListTemplates_NullNormalised(t *testing.T) {
	ts := testServer(t, map[string]http.HandlerFunc{
		"/templates": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "null")
		},
	})
	defer ts.Close()

	c := New(ts.URL)
	tpls, err := c.ListTemplates(context.Background())
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if tpls == nil {
		t.Fatal("expected non-nil empty slice")
	}
}

func TestGetTemplate(t *testing.T) {
	ts := testServer(t, map[string]http.HandlerFunc{
		"/templates/t-abc": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				writeErr(t, w, http.StatusMethodNotAllowed, "bad method")
				return
			}
			writeJSON(t, w, http.StatusOK, TemplateInfo{
				TemplateID: "t-abc",
				CPUCount:   2,
				MemoryMB:   512,
			})
		},
	})
	defer ts.Close()

	c := New(ts.URL)
	tpl, err := c.GetTemplate(context.Background(), "t-abc")
	if err != nil {
		t.Fatalf("GetTemplate: %v", err)
	}
	if tpl.TemplateID != "t-abc" {
		t.Errorf("TemplateID = %q, want %q", tpl.TemplateID, "t-abc")
	}
}

func TestDeleteTemplate(t *testing.T) {
	var called bool
	ts := testServer(t, map[string]http.HandlerFunc{
		"/templates/t-abc": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete {
				writeErr(t, w, http.StatusMethodNotAllowed, "bad method")
				return
			}
			called = true
			w.WriteHeader(http.StatusNoContent)
		},
	})
	defer ts.Close()

	c := New(ts.URL)
	if err := c.DeleteTemplate(context.Background(), "t-abc"); err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	if !called {
		t.Error("handler not called")
	}
}

// -----------------------------------------------------------------------
// Error handling
// -----------------------------------------------------------------------

func TestAPIError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  APIError
		want string
	}{
		{
			name: "with message",
			err:  APIError{StatusCode: 404, Message: "not found"},
			want: "e2bgateway: 404 not found",
		},
		{
			name: "without message",
			err:  APIError{StatusCode: 500},
			want: "e2bgateway: unexpected status 500",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServerReturnsUnexpectedStatus(t *testing.T) {
	ts := testServer(t, map[string]http.HandlerFunc{
		"/healthz": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "plain text error body")
		},
	})
	defer ts.Close()

	c := New(ts.URL)
	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr := err.(*APIError)
	if apiErr.StatusCode != http.StatusBadGateway {
		t.Errorf("StatusCode = %d, want 502", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Message, "plain text") {
		t.Errorf("Message = %q, want substring %q", apiErr.Message, "plain text")
	}
}

func TestServerReturnsEmptyErrorBody(t *testing.T) {
	ts := testServer(t, map[string]http.HandlerFunc{
		"/healthz": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		},
	})
	defer ts.Close()

	c := New(ts.URL)
	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr := err.(*APIError)
	if apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("StatusCode = %d, want 503", apiErr.StatusCode)
	}
}

// -----------------------------------------------------------------------
// Context propagation & network errors
// -----------------------------------------------------------------------

func TestContextCancellation(t *testing.T) {
	ts := testServer(t, map[string]http.HandlerFunc{
		"/healthz": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusOK, map[string]string{"status": "ok"})
		},
	})
	defer ts.Close()

	c := New(ts.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := c.Health(ctx)
	if err == nil {
		t.Fatal("expected error due to canceled context")
	}
}

func TestConnectionRefused(t *testing.T) {
	// Use a port that is (almost certainly) not in use.
	c := New("http://127.0.0.1:1")
	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

// -----------------------------------------------------------------------
// URL path escaping
// -----------------------------------------------------------------------

func TestURLPathEscape(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"with-dash", "with-dash"},
		{"with_underscore", "with_underscore"},
		{"with.dot", "with.dot"},
		{"with~tilde", "with~tilde"},
		{"with space", "with%20space"},
		{"with/slash", "with%2Fslash"},
		{"with+plus", "with%2Bplus"},
	}
	for _, tt := range tests {
		got := urlPathEscape(tt.input)
		if got != tt.want {
			t.Errorf("urlPathEscape(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// -----------------------------------------------------------------------
// Full round-trip integration: server that validates API key
// -----------------------------------------------------------------------

func TestFullRoundTrip_WithAPIKey(t *testing.T) {
	var requestCount atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)

		// Verify API key on all requests.
		if r.Header.Get("X-API-Key") != "test-key" {
			writeErr(t, w, http.StatusUnauthorized, "missing api key")
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/healthz":
			writeJSON(t, w, http.StatusOK, map[string]string{"status": "ok"})

		case r.Method == http.MethodPost && r.URL.Path == "/sandboxes":
			writeJSON(t, w, http.StatusCreated, SandboxCreateResponse{
				SandboxID:  "sb-it",
				TemplateID: "base",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/sandboxes":
			writeJSON(t, w, http.StatusOK, []SandboxInfo{
				{SandboxID: "sb-it", State: "running"},
			})

		case r.Method == http.MethodGet && r.URL.Path == "/sandboxes/sb-it":
			writeJSON(t, w, http.StatusOK, SandboxInfo{
				SandboxID: "sb-it", State: "running",
			})

		case r.Method == http.MethodDelete && r.URL.Path == "/sandboxes/sb-it":
			w.WriteHeader(http.StatusNoContent)

		default:
			writeErr(t, w, http.StatusNotFound, "not found: "+r.URL.Path)
		}
	}))
	defer ts.Close()

	c := New(ts.URL, WithAPIKey("test-key"))
	ctx := context.Background()

	// Health
	hs, err := c.Health(ctx)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if hs.Status != "ok" {
		t.Errorf("health status = %q", hs.Status)
	}

	// Create
	cr, err := c.CreateSandbox(ctx, CreateSandboxRequest{TemplateID: "base"})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	if cr.SandboxID != "sb-it" {
		t.Errorf("SandboxID = %q", cr.SandboxID)
	}

	// List
	sbs, err := c.ListSandboxes(ctx)
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if len(sbs) != 1 || sbs[0].SandboxID != "sb-it" {
		t.Errorf("ListSandboxes = %+v", sbs)
	}

	// Get
	sb, err := c.GetSandbox(ctx, "sb-it")
	if err != nil {
		t.Fatalf("GetSandbox: %v", err)
	}
	if sb.State != "running" {
		t.Errorf("State = %q", sb.State)
	}

	// Kill
	if err := c.KillSandbox(ctx, "sb-it"); err != nil {
		t.Fatalf("KillSandbox: %v", err)
	}

	if cnt := requestCount.Load(); cnt != 5 {
		t.Errorf("expected 5 requests, got %d", cnt)
	}
}

// -----------------------------------------------------------------------
// drain helper
// -----------------------------------------------------------------------

func TestDrain(t *testing.T) {
	// Ensure drain does not panic on nil response.
	drain(nil)

	// Drain a response with an open body.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "body")
	}))
	defer ts.Close()

	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	drain(resp)
	// A second drain should also be safe.
	drain(resp)
}
