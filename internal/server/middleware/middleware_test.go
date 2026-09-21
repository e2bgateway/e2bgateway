package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/e2bgateway/e2bgateway/internal/api/dto"
	"github.com/e2bgateway/e2bgateway/internal/auth"
	"github.com/e2bgateway/e2bgateway/internal/config"
)

func TestAuthMiddleware_SkipsHealth(t *testing.T) {
	handler := Auth(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("path %s: expected 200, got %d", path, rr.Code)
		}
	}
}

func TestAuthMiddleware_ValidKey(t *testing.T) {
	mgr := auth.NewManager(config.AuthConfig{
		Providers: []config.AuthProviderConfig{
			{Type: "apikey", SecretRef: "test", HeaderName: "X-API-Key"},
		},
	})

	var capturedTenant string
	handler := Auth(mgr)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tc, ok := auth.TenantFromContext(r.Context()); ok {
			capturedTenant = tc.TenantID
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes", nil)
	req.Header.Set("X-API-Key", "test-key-1")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	if capturedTenant != "test-tenant" {
		t.Errorf("expected tenant 'test-tenant', got %q", capturedTenant)
	}
}

func TestAuthMiddleware_InvalidKey(t *testing.T) {
	mgr := auth.NewManager(config.AuthConfig{
		Providers: []config.AuthProviderConfig{
			{Type: "apikey", SecretRef: "test", HeaderName: "X-API-Key"},
		},
	})

	handler := Auth(mgr)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called on auth failure")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes", nil)
	req.Header.Set("X-API-Key", "invalid")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestRateLimitMiddleware_Allows(t *testing.T) {
	cfg := config.RateLimitConfig{
		Enabled: true,
		DefaultLimit: config.RateLimitDefaultConfig{
			RequestsPerMinute: 600,
			BurstSize:         100,
		},
	}

	handler := RateLimit(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestRateLimitMiddleware_Exceeds(t *testing.T) {
	cfg := config.RateLimitConfig{
		Enabled: true,
		DefaultLimit: config.RateLimitDefaultConfig{
			RequestsPerMinute: 60,
			BurstSize:         2,
		},
	}

	handler := RateLimit(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Exhaust burst
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes", nil)
		req.RemoteAddr = "1.2.3.4:1234"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("request %d: expected 200, got %d", i, rr.Code)
		}
	}

	// This should be rate limited
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", rr.Code)
	}
	assert.Equal(t, "60", rr.Header().Get("Retry-After"))
	assertE2BErrorResponse(t, rr, http.StatusTooManyRequests, "Rate limit exceeded. Please retry after some time.")
}

func TestCORS(t *testing.T) {
	handler := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// OPTIONS request
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/sandboxes", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("expected 204 for OPTIONS, got %d", rr.Code)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("expected CORS origin header")
	}

	// Normal request
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes", nil)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr2.Code)
	}
}

func TestRecovery(t *testing.T) {
	handler := Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assertE2BErrorResponse(t, rr, http.StatusInternalServerError, "Internal Server Error")
}

func TestRealIP(t *testing.T) {
	var capturedAddr string
	handler := RealIP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAddr = r.RemoteAddr
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if capturedAddr != "1.2.3.4:0" {
		t.Errorf("expected '1.2.3.4:0', got %q", capturedAddr)
	}
}

func TestWriteJSON(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteJSON(rr, http.StatusOK, map[string]string{"hello": "world"})

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	if rr.Header().Get("Content-Type") != "application/json" {
		t.Error("expected Content-Type application/json")
	}
}

func TestWriteError(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteError(rr, http.StatusNotFound, "resource not found")

	assertE2BErrorResponse(t, rr, http.StatusNotFound, "resource not found")
}

func assertE2BErrorResponse(t *testing.T, rr *httptest.ResponseRecorder, wantStatus int, wantMessage string) {
	t.Helper()

	require.Equal(t, wantStatus, rr.Code)
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

	var got dto.ErrorResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&got))
	assert.Equal(t, dto.ErrorResponse{Code: wantStatus, Message: wantMessage}, got)
}
