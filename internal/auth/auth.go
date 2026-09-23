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

// HasScope reports whether an authenticated tenant may perform an operation.
func HasScope(tc *TenantContext, required string) bool {
	if tc == nil || required == "" {
		return false
	}
	for _, scope := range tc.Scopes {
		if scope == "*" || scope == required {
			return true
		}
	}
	return false
}

// NewManager creates a new auth Manager.
func NewManager(cfg config.AuthConfig) (*Manager, error) {
	m := &Manager{cfg: cfg}

	for _, pCfg := range cfg.Providers {
		switch pCfg.Type {
		case "apikey":
			p := NewAPIKeyProvider(pCfg)
			m.providers = append(m.providers, p)
		case "jwt":
			p, err := NewJWTProvider(pCfg)
			if err != nil {
				return nil, fmt.Errorf("configuring JWT provider: %w", err)
			}
			m.providers = append(m.providers, p)
		default:
			return nil, fmt.Errorf("unsupported auth provider type %q", pCfg.Type)
		}
	}

	return m, nil
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
	if len(cfg.Keys) > 0 {
		for i, key := range cfg.Keys {
			p.keys[key] = &TenantContext{
				TenantID: fmt.Sprintf("tenant-%d", i),
				APIKeyID: fmt.Sprintf("key-%d", i),
				Scopes:   []string{"*"},
			}
		}
	} else if cfg.SecretRef != "" {
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
