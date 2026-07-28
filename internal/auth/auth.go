// Package auth implements authentication and tenant management for E2BGateway.
package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/config"
)

// contextKey is a private type for context keys.
type contextKey string

const tenantContextKey contextKey = "tenant"

// Manager manages authentication providers.
type Manager struct {
	cfg       config.AuthConfig
	providers []Provider
	mu        sync.RWMutex
}

// Provider is the interface for authentication providers.
type Provider interface {
	// Authenticate validates credentials and returns a tenant context.
	Authenticate(r *http.Request) (*TenantContext, error)
	// Name returns the provider name.
	Name() string
}

// TenantContext carries authenticated tenant information through the request.
type TenantContext struct {
	TenantID string            `json:"tenantID"`
	APIKeyID string            `json:"apiKeyID,omitempty"`
	Scopes   []string          `json:"scopes,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// NewManager creates a new auth Manager.
func NewManager(cfg config.AuthConfig) *Manager {
	m := &Manager{cfg: cfg}

	for _, pCfg := range cfg.Providers {
		switch pCfg.Type {
		case "apikey":
			p := NewAPIKeyProvider(pCfg)
			m.providers = append(m.providers, p)
		case "jwt":
			p := NewJWTProvider(pCfg)
			m.providers = append(m.providers, p)
		}
	}

	return m
}

// Authenticate tries each provider in order and returns the first successful result.
func (m *Manager) Authenticate(r *http.Request) (*TenantContext, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, p := range m.providers {
		tc, err := p.Authenticate(r)
		if err == nil && tc != nil {
			return tc, nil
		}
	}

	// If no providers configured, allow anonymous access
	if len(m.providers) == 0 {
		return &TenantContext{TenantID: "anonymous"}, nil
	}

	return nil, fmt.Errorf("authentication failed: no valid credentials")
}

// TenantFromContext extracts the tenant context from the request context.
func TenantFromContext(ctx context.Context) (*TenantContext, bool) {
	tc, ok := ctx.Value(tenantContextKey).(*TenantContext)
	return tc, ok
}

// WithTenant adds a tenant context to the request context.
func WithTenant(ctx context.Context, tc *TenantContext) context.Context {
	return context.WithValue(ctx, tenantContextKey, tc)
}

// --- API Key Provider ---

// APIKeyProvider authenticates using API keys from a static map or configurable store.
type APIKeyProvider struct {
	cfg  config.AuthProviderConfig
	keys map[string]*TenantContext
	mu   sync.RWMutex
}

// NewAPIKeyProvider creates a new API key provider.
func NewAPIKeyProvider(cfg config.AuthProviderConfig) *APIKeyProvider {
	p := &APIKeyProvider{
		cfg:  cfg,
		keys: make(map[string]*TenantContext),
	}

	// Load static keys from config
	// In production, these would come from a secret/K8s secret ref
	if cfg.SecretRef != "" {
		// Pre-populate with some default keys for testing
		p.keys["test-key-1"] = &TenantContext{
			TenantID: "test-tenant",
			APIKeyID: "key-1",
			Scopes:   []string{"sandbox:read", "sandbox:write", "template:read"},
		}
		p.keys["test-key-admin"] = &TenantContext{
			TenantID: "admin-tenant",
			APIKeyID: "key-admin",
			Scopes:   []string{"*"},
		}
	}

	return p
}

// Name returns the provider name.
func (p *APIKeyProvider) Name() string {
	return "apikey"
}

// Authenticate extracts and validates the API key from the request.
func (p *APIKeyProvider) Authenticate(r *http.Request) (*TenantContext, error) {
	// Try the configured header
	headerName := p.cfg.HeaderName
	if headerName == "" {
		headerName = "X-API-Key"
	}

	key := r.Header.Get(headerName)

	// Try alternate header (e.g., Authorization)
	if key == "" && p.cfg.AlternateHeader != "" {
		authHeader := r.Header.Get(p.cfg.AlternateHeader)
		if p.cfg.BearerPrefix && strings.HasPrefix(authHeader, "Bearer ") {
			key = strings.TrimPrefix(authHeader, "Bearer ")
		} else if !p.cfg.BearerPrefix {
			key = authHeader
		}
	}

	if key == "" {
		return nil, fmt.Errorf("no API key provided")
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	tc, ok := p.keys[key]
	if !ok {
		return nil, fmt.Errorf("invalid API key")
	}

	return tc, nil
}

// AddKey registers an API key for a tenant.
func (p *APIKeyProvider) AddKey(key string, tc *TenantContext) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keys[key] = tc
}

// RemoveKey removes an API key.
func (p *APIKeyProvider) RemoveKey(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.keys, key)
}

// --- JWT Provider ---

// JWTProvider authenticates using JWT tokens.
type JWTProvider struct {
	cfg config.AuthProviderConfig
}

// NewJWTProvider creates a new JWT provider.
func NewJWTProvider(cfg config.AuthProviderConfig) *JWTProvider {
	return &JWTProvider{cfg: cfg}
}

// Name returns the provider name.
func (p *JWTProvider) Name() string {
	return "jwt"
}

// Authenticate extracts and validates a JWT token from the Authorization header.
func (p *JWTProvider) Authenticate(r *http.Request) (*TenantContext, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil, fmt.Errorf("no authorization header")
	}

	if !strings.HasPrefix(authHeader, "Bearer ") {
		return nil, fmt.Errorf("invalid authorization header format")
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == "" {
		return nil, fmt.Errorf("empty token")
	}

	// In production: validate signature, issuer, audience, expiry
	// For now, parse the token as a simple JWT-like structure
	return p.validateToken(token)
}

// validateToken validates a JWT token.
// In production, this would use a proper JWT library with JWKS verification.
func (p *JWTProvider) validateToken(token string) (*TenantContext, error) {
	// Simple mock validation: token format is "tenant-id.scopes"
	parts := strings.SplitN(token, ".", 2)
	if len(parts) < 1 {
		return nil, fmt.Errorf("invalid token format")
	}

	tenantID := parts[0]
	if tenantID == "" || tenantID == "invalid" {
		return nil, fmt.Errorf("invalid tenant in token")
	}

	var scopes []string
	if len(parts) == 2 {
		scopes = strings.Split(parts[1], ",")
	}

	return &TenantContext{
		TenantID: tenantID,
		Scopes:   scopes,
	}, nil
}

// --- Rate Limiter ---

// RateLimiter implements a simple token bucket rate limiter.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	cfg     config.RateLimitConfig
}

type bucket struct {
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
}

// NewRateLimiter creates a new rate limiter.
func NewRateLimiter(cfg config.RateLimitConfig) *RateLimiter {
	return &RateLimiter{
		buckets: make(map[string]*bucket),
		cfg:     cfg,
	}
}

// Allow checks if a request from the given key is allowed.
func (rl *RateLimiter) Allow(key string) bool {
	if !rl.cfg.Enabled {
		return true
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.buckets[key]
	if !ok {
		// Check for tenant-specific overrides
		rpm := rl.cfg.DefaultLimit.RequestsPerMinute
		burst := rl.cfg.DefaultLimit.BurstSize

		b = &bucket{
			tokens:     float64(burst),
			maxTokens:  float64(burst),
			refillRate: float64(rpm) / 60.0,
			lastRefill: time.Now(),
		}
		rl.buckets[key] = b
	}

	// Refill tokens
	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * b.refillRate
	if b.tokens > b.maxTokens {
		b.tokens = b.maxTokens
	}
	b.lastRefill = now

	// Check if allowed
	if b.tokens >= 1 {
		b.tokens--
		return true
	}

	return false
}

// --- Middleware Helpers ---

// ContextWithTenant adds tenant info to the request context.
func ContextWithTenant(r *http.Request, tc *TenantContext) *http.Request {
	return r.WithContext(WithTenant(r.Context(), tc))
}

// CompareKeys does a constant-time comparison of two API keys.
func CompareKeys(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// WriteJSONError writes a JSON error response.
func WriteJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":    code,
		"message": message,
	})
}
