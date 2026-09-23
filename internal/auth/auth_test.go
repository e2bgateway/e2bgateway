package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

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

func jwtFixture(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
	raw, err := json.Marshal(map[string]any{
		"keys": []map[string]string{{"kty": "RSA", "kid": "test-key", "alg": "RS256", "use": "sig", "n": n, "e": e}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return key, string(raw)
}

func signedJWT(t *testing.T, key *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key"
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestJWTProvider_Authenticate(t *testing.T) {
	key, jwks := jwtFixture(t)
	p, err := NewJWTProvider(config.AuthProviderConfig{
		Type: "jwt", Issuer: "https://auth.example.com", Audience: "e2bgateway",
		JWKS: jwks,
	})
	if err != nil {
		t.Fatal(err)
	}
	base := func() jwt.MapClaims {
		return jwt.MapClaims{
			"sub": "my-tenant", "scope": "sandbox:read template:read", "iss": "https://auth.example.com",
			"aud": "e2bgateway", "exp": time.Now().Add(time.Hour).Unix(),
		}
	}

	valid := signedJWT(t, key, base())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+valid)
	tc, err := p.Authenticate(req)
	if err != nil || tc.TenantID != "my-tenant" || len(tc.Scopes) != 2 {
		t.Fatalf("valid JWT: tenant=%+v, err=%v", tc, err)
	}

	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		make func() string
	}{
		{"expired", func() string {
			c := base()
			c["exp"] = time.Now().Add(-time.Minute).Unix()
			return signedJWT(t, key, c)
		}},
		{"missing expiry", func() string { c := base(); delete(c, "exp"); return signedJWT(t, key, c) }},
		{"future not-before", func() string { c := base(); c["nbf"] = time.Now().Add(time.Hour).Unix(); return signedJWT(t, key, c) }},
		{"wrong issuer", func() string { c := base(); c["iss"] = "other"; return signedJWT(t, key, c) }},
		{"wrong audience", func() string { c := base(); c["aud"] = "other"; return signedJWT(t, key, c) }},
		{"missing subject", func() string { c := base(); delete(c, "sub"); return signedJWT(t, key, c) }},
		{"bad signature", func() string { return signedJWT(t, otherKey, base()) }},
		{"unknown key ID", func() string {
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, base())
			token.Header["kid"] = "unknown"
			raw, signErr := token.SignedString(key)
			if signErr != nil {
				t.Fatal(signErr)
			}
			return raw
		}},
		{"missing key ID", func() string {
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, base())
			raw, signErr := token.SignedString(key)
			if signErr != nil {
				t.Fatal(signErr)
			}
			return raw
		}},
		{"disallowed algorithm", func() string {
			token := jwt.NewWithClaims(jwt.SigningMethodHS256, base())
			token.Header["kid"] = "test-key"
			raw, signErr := token.SignedString([]byte("wrong-key-type"))
			if signErr != nil {
				t.Fatal(signErr)
			}
			return raw
		}},
		{"fake old format", func() string { return "my-tenant.sandbox:read" }},
		{"missing token", func() string { return "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if raw := tt.make(); raw != "" {
				r.Header.Set("Authorization", "Bearer "+raw)
			}
			if _, err := p.Authenticate(r); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestJWTProvider_StaticJWKSValidation(t *testing.T) {
	_, valid := jwtFixture(t)
	configFor := func(jwks string) config.AuthProviderConfig {
		return config.AuthProviderConfig{Type: "jwt", Issuer: "issuer", Audience: "audience", JWKS: jwks}
	}
	for _, tt := range []struct{ name, jwks string }{
		{"empty set", `{"keys":[]}`},
		{"invalid JSON", `{"keys":`},
		{"symmetric secret", `{"keys":[{"kid":"one","kty":"oct","alg":"HS256","k":"c2VjcmV0"}]}`},
		{"private key", `{"keys":[{"kid":"one","kty":"RSA","alg":"RS256","d":"secret"}]}`},
		{"duplicate kid", `{"keys":[{"kid":"one","kty":"RSA","alg":"RS256"},{"kid":"one","kty":"RSA","alg":"RS256"}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewJWTProvider(configFor(tt.jwks)); err == nil {
				t.Fatal("expected startup rejection")
			}
		})
	}
	if _, err := NewJWTProvider(configFor(valid)); err != nil {
		t.Fatal(err)
	}
}

func TestJWTProvider_ES256(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "EC", "kid": "ec-key", "alg": "ES256", "use": "sig", "crv": "P-256",
		"x": base64.RawURLEncoding.EncodeToString(key.X.FillBytes(make([]byte, 32))),
		"y": base64.RawURLEncoding.EncodeToString(key.Y.FillBytes(make([]byte, 32))),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewJWTProvider(config.AuthProviderConfig{Type: "jwt", Issuer: "issuer", Audience: "audience", JWKS: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"sub": "tenant", "scope": "sandbox:read", "iss": "issuer", "aud": "audience", "exp": time.Now().Add(time.Hour).Unix(),
	})
	token.Header["kid"] = "ec-key"
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+signed)
	if got, err := p.Authenticate(r); err != nil || got.TenantID != "tenant" {
		t.Fatalf("ES256 JWT: tenant=%+v, err=%v", got, err)
	}
}

func TestManager_Authenticate(t *testing.T) {
	mgr, err := NewManager(config.AuthConfig{
		Providers: []config.AuthProviderConfig{
			{Type: "apikey", SecretRef: "test", HeaderName: "X-API-Key"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

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
	mgr, err := NewManager(config.AuthConfig{})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	tc, err := mgr.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error with no providers, got: %v", err)
	}
	if tc.TenantID != "anonymous" {
		t.Errorf("expected anonymous tenant, got %s", tc.TenantID)
	}
}

func TestManager_RejectsUnsupportedProvider(t *testing.T) {
	if _, err := NewManager(config.AuthConfig{Providers: []config.AuthProviderConfig{{Type: "unknown"}}}); err == nil {
		t.Fatal("unsupported auth provider must not silently enable anonymous access")
	}
}

func TestHasScope(t *testing.T) {
	for _, tt := range []struct {
		name     string
		tenant   *TenantContext
		required string
		want     bool
	}{
		{"exact", &TenantContext{Scopes: []string{"sandbox:read"}}, "sandbox:read", true},
		{"wildcard", &TenantContext{Scopes: []string{"*"}}, "template:write", true},
		{"different", &TenantContext{Scopes: []string{"template:read"}}, "sandbox:read", false},
		{"empty", &TenantContext{}, "sandbox:read", false},
		{"nil", nil, "sandbox:read", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasScope(tt.tenant, tt.required); got != tt.want {
				t.Fatalf("HasScope() = %v, want %v", got, tt.want)
			}
		})
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
