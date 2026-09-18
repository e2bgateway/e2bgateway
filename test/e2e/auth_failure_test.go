package e2e

import (
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
	"github.com/e2bgateway/e2bgateway/internal/server"
)

func authFixture(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
	jwks, err := json.Marshal(map[string]any{
		"keys": []map[string]string{{"kty": "RSA", "kid": "e2e-key", "alg": "RS256", "use": "sig", "n": n, "e": e}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return key, string(jwks)
}

func authToken(t *testing.T, key *rsa.PrivateKey, scope string, expiry time.Time) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": "e2e-tenant", "scope": scope, "iss": "https://issuer.example.test",
		"aud": "e2bgateway", "exp": expiry.Unix(),
	})
	token.Header["kid"] = "e2e-key"
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func authGateway(t *testing.T, jwks string, rateLimit bool) *httptest.Server {
	t.Helper()
	cfg := &config.Config{
		Server:   config.ServerConfig{HTTP: config.HTTPConfig{Address: "127.0.0.1:0"}},
		Backends: []config.BackendConfig{{Name: "mock", Type: "mock", Enabled: true}},
		Routing:  config.RoutingConfig{DefaultBackend: "mock", Strategy: "static"},
		Auth: config.AuthConfig{Providers: []config.AuthProviderConfig{
			{Type: "apikey", Keys: []string{"valid-api-key"}},
			{Type: "jwt", Issuer: "https://issuer.example.test", Audience: "e2bgateway", JWKS: jwks},
		}},
		RateLimit: config.RateLimitConfig{Enabled: rateLimit, DefaultLimit: config.RateLimitDefaultConfig{RequestsPerMinute: 1, BurstSize: 1}},
	}
	srv, err := server.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func authRequest(t *testing.T, ts *httptest.Server, method, path, apiKey, bearer string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func assertAuthError(t *testing.T, resp *http.Response, wantStatus int, wantType string) {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("status: got %d, want %d", resp.StatusCode, wantStatus)
	}
	var body struct {
		Error struct {
			Code int    `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != wantStatus || body.Error.Type != wantType {
		t.Fatalf("error body: got %+v", body.Error)
	}
}

func TestAuthFailureE2E(t *testing.T) {
	key, jwks := authFixture(t)
	ts := authGateway(t, jwks, false)
	valid := authToken(t, key, "sandbox:read", time.Now().Add(time.Hour))
	expired := authToken(t, key, "sandbox:read", time.Now().Add(-time.Minute))
	insufficient := authToken(t, key, "template:read", time.Now().Add(time.Hour))

	for _, path := range []string{"/sandboxes", "/v2/sandboxes", "/api/v1/sandboxes"} {
		t.Run(path, func(t *testing.T) {
			assertAuthError(t, authRequest(t, ts, http.MethodGet, path, "", ""), http.StatusUnauthorized, "Unauthorized")
			assertAuthError(t, authRequest(t, ts, http.MethodGet, path, "wrong", ""), http.StatusUnauthorized, "Unauthorized")
			assertAuthError(t, authRequest(t, ts, http.MethodGet, path, "", expired), http.StatusUnauthorized, "Unauthorized")
			assertAuthError(t, authRequest(t, ts, http.MethodGet, path, "", insufficient), http.StatusForbidden, "Forbidden")

			for _, creds := range []struct{ apiKey, bearer string }{{"valid-api-key", ""}, {"", valid}} {
				resp := authRequest(t, ts, http.MethodGet, path, creds.apiKey, creds.bearer)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("valid credentials: got %d for %s", resp.StatusCode, path)
				}
			}
		})
	}

	// A write operation must not be downgraded to a read-scope operation.
	assertAuthError(t, authRequest(t, ts, http.MethodPost, "/sandboxes", "", valid), http.StatusForbidden, "Forbidden")
	assertAuthError(t, authRequest(t, ts, http.MethodGet, "/sandboxes/id/ws", "", valid), http.StatusForbidden, "Forbidden")
	assertAuthError(t, authRequest(t, ts, http.MethodPost, "/sandboxes/id/access-token", "", valid), http.StatusForbidden, "Forbidden")
	templateReader := authToken(t, key, "template:read", time.Now().Add(time.Hour))
	resp := authRequest(t, ts, http.MethodGet, "/templates", "", templateReader)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("template read scope: got %d", resp.StatusCode)
	}
	assertAuthError(t, authRequest(t, ts, http.MethodPost, "/templates", "", templateReader), http.StatusForbidden, "Forbidden")
	assertAuthError(t, authRequest(t, ts, http.MethodGet, "/warm-pools", "", templateReader), http.StatusForbidden, "Forbidden")

	// The forbidden sandbox creation must not reach the mock backend.
	resp = authRequest(t, ts, http.MethodGet, "/sandboxes", "valid-api-key", "")
	var sandboxes []map[string]any
	decodeJSON(t, resp, &sandboxes)
	if len(sandboxes) != 0 {
		t.Fatalf("forbidden creation changed backend state: %d sandboxes", len(sandboxes))
	}

	// The health endpoint remains public.
	resp = authRequest(t, ts, http.MethodGet, "/healthz", "", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status: %d", resp.StatusCode)
	}
}

func TestRateLimitE2E(t *testing.T) {
	_, jwks := authFixture(t)
	ts := authGateway(t, jwks, true)
	first := authRequest(t, ts, http.MethodGet, "/sandboxes", "valid-api-key", "")
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first request: got %d", first.StatusCode)
	}
	second := authRequest(t, ts, http.MethodGet, "/sandboxes", "valid-api-key", "")
	if second.Header.Get("Retry-After") == "" {
		t.Error("missing Retry-After")
	}
	assertAuthError(t, second, http.StatusTooManyRequests, "RateLimitExceeded")
	assertAuthError(t, authRequest(t, ts, http.MethodGet, "/sandboxes", "", ""), http.StatusUnauthorized, "Unauthorized")
}
