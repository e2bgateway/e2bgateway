package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e2bgateway/e2bgateway/internal/config"
)

func TestAPIKeyProvider_Authenticate(t *testing.T) {
	p := NewAPIKeyProvider(config.AuthProviderConfig{
		Type:       "apikey",
		SecretRef:  "test-secret",
		HeaderName: "X-API-Key",
	})

	// Test with valid key
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "test-key-1")

	tc, err := p.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if tc.TenantID != "test-tenant" {
		t.Errorf("expected tenant 'test-tenant', got %s", tc.TenantID)
	}

	// Test with invalid key
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("X-API-Key", "invalid-key")

	_, err = p.Authenticate(req2)
	if err == nil {
		t.Error("expected error for invalid key")
	}

	// Test with missing key
	req3 := httptest.NewRequest(http.MethodGet, "/", nil)
	_, err = p.Authenticate(req3)
	if err == nil {
		t.Error("expected error for missing key")
	}
}

func TestAPIKeyProvider_BearerAuth(t *testing.T) {
	p := NewAPIKeyProvider(config.AuthProviderConfig{
		Type:            "apikey",
		SecretRef:       "test-secret",
		HeaderName:      "X-API-Key",
		AlternateHeader: "Authorization",
		BearerPrefix:    true,
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer test-key-1")

	tc, err := p.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if tc.TenantID != "test-tenant" {
		t.Errorf("expected tenant 'test-tenant', got %s", tc.TenantID)
	}
}

func TestAPIKeyProvider_AddRemoveKey(t *testing.T) {
	p := NewAPIKeyProvider(config.AuthProviderConfig{
		Type:       "apikey",
		HeaderName: "X-API-Key",
	})

	// Add a key
	p.AddKey("custom-key", &TenantContext{
		TenantID: "custom-tenant",
		Scopes:   []string{"read"},
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "custom-key")

	tc, err := p.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if tc.TenantID != "custom-tenant" {
		t.Errorf("expected tenant 'custom-tenant', got %s", tc.TenantID)
	}

	// Remove key
	p.RemoveKey("custom-key")
	_, err = p.Authenticate(req)
	if err == nil {
		t.Error("expected error after removing key")
	}
}

func TestJWTProvider_Authenticate(t *testing.T) {
	p := NewJWTProvider(config.AuthProviderConfig{
		Type:     "jwt",
		Issuer:   "https://auth.example.com",
		Audience: "e2bgateway",
	})

	// Valid token (simple format: tenant.scopes)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer my-tenant.read,write")

	tc, err := p.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if tc.TenantID != "my-tenant" {
		t.Errorf("expected tenant 'my-tenant', got %s", tc.TenantID)
	}
	if len(tc.Scopes) != 2 {
		t.Errorf("expected 2 scopes, got %d", len(tc.Scopes))
	}

	// Invalid token
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("Authorization", "Bearer invalid.read")
	// 'invalid' is treated as invalid tenant
	_, err = p.Authenticate(req2)
	if err == nil {
		t.Error("expected error for 'invalid' tenant")
	}

	// Missing auth header
	req3 := httptest.NewRequest(http.MethodGet, "/", nil)
	_, err = p.Authenticate(req3)
	if err == nil {
		t.Error("expected error for missing auth header")
	}
}

func TestManager_Authenticate(t *testing.T) {
	mgr := NewManager(config.AuthConfig{
		Providers: []config.AuthProviderConfig{
			{Type: "apikey", SecretRef: "test", HeaderName: "X-API-Key"},
		},
	})

	// Valid key
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "test-key-1")

	tc, err := mgr.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if tc.TenantID != "test-tenant" {
		t.Errorf("expected tenant 'test-tenant', got %s", tc.TenantID)
	}

	// Invalid key
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("X-API-Key", "wrong")
	_, err = mgr.Authenticate(req2)
	if err == nil {
		t.Error("expected error for invalid key")
	}
}

func TestManager_NoProviders(t *testing.T) {
	mgr := NewManager(config.AuthConfig{})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	tc, err := mgr.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error with no providers, got: %v", err)
	}
	if tc.TenantID != "anonymous" {
		t.Errorf("expected anonymous tenant, got %s", tc.TenantID)
	}
}

func TestRateLimiter(t *testing.T) {
	rl := NewRateLimiter(config.RateLimitConfig{
		Enabled: true,
		DefaultLimit: config.RateLimitDefaultConfig{
			RequestsPerMinute: 60, // 1 per second
			BurstSize:         5,
		},
	})

	// Should allow burst
	for i := 0; i < 5; i++ {
		if !rl.Allow("test-key") {
			t.Errorf("request %d should be allowed", i)
		}
	}

	// 6th request should be denied (burst exhausted)
	if rl.Allow("test-key") {
		t.Error("expected rate limit to kick in after burst")
	}

	// Different key should still be allowed
	if !rl.Allow("other-key") {
		t.Error("expected other key to be allowed")
	}
}

func TestRateLimiter_Disabled(t *testing.T) {
	rl := NewRateLimiter(config.RateLimitConfig{
		Enabled: false,
	})

	for i := 0; i < 100; i++ {
		if !rl.Allow("test-key") {
			t.Error("expected all requests to be allowed when disabled")
		}
	}
}

func TestTenantContext(t *testing.T) {
	tc := &TenantContext{
		TenantID: "test",
		Scopes:   []string{"read"},
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = ContextWithTenant(req, tc)

	got, ok := TenantFromContext(req.Context())
	if !ok {
		t.Fatal("expected tenant context to be set")
	}
	if got.TenantID != "test" {
		t.Errorf("expected tenant 'test', got %s", got.TenantID)
	}
}

func TestCompareKeys(t *testing.T) {
	if !CompareKeys("abc", "abc") {
		t.Error("expected equal keys to match")
	}
	if CompareKeys("abc", "def") {
		t.Error("expected different keys to not match")
	}
}
