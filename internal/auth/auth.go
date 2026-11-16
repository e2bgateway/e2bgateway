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

package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/config"
)

// contextKey is a private type for context keys.
type contextKey string

const tenantContextKey contextKey = "tenant"

const (
	providerTypeAPIKey  = "apikey"
	providerTypeJWT     = "jwt"
	defaultAPIKeyHeader = "X-API-Key"
)

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
		case providerTypeAPIKey:
			p, err := NewAPIKeyProvider(pCfg)
			if err != nil {
				return nil, fmt.Errorf("configuring apikey provider: %w", err)
			}
			m.providers = append(m.providers, p)
		case providerTypeJWT:
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

// NewAPIKeyProvider creates a new API key provider. Keys come from the static
// cfg.Keys list or, when cfg.SecretRef is set, from the JSON file at that path
// (see loadKeysFromSecretFile). An unresolvable secretRef is an error — the
// caller (NewManager) fails startup rather than serving without the operator's
// configured keys.
func NewAPIKeyProvider(cfg config.AuthProviderConfig) (*APIKeyProvider, error) {
	p := &APIKeyProvider{
		cfg:  cfg,
		keys: make(map[string]*TenantContext),
	}

	switch {
	case len(cfg.Keys) > 0:
		// Load static keys from config
		for i, key := range cfg.Keys {
			p.keys[key] = &TenantContext{
				TenantID: fmt.Sprintf("tenant-%d", i),
				APIKeyID: fmt.Sprintf("key-%d", i),
				Scopes:   []string{"*"},
			}
		}
	case cfg.SecretRef != "":
		keys, err := loadKeysFromSecretFile(cfg.SecretRef)
		if err != nil {
			return nil, fmt.Errorf("loading secretRef API keys: %w", err)
		}
		p.keys = keys
	}

	return p, nil
}

// Name returns the provider name.
func (p *APIKeyProvider) Name() string {
	return providerTypeAPIKey
}

// Authenticate extracts and validates the API key from the request.
func (p *APIKeyProvider) Authenticate(r *http.Request) (*TenantContext, error) {
	// Try the configured header
	headerName := p.cfg.HeaderName
	if headerName == "" {
		headerName = defaultAPIKeyHeader
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

// --- secretRef file loading ---

// secretKeyEntry is one API key's tenant context as stored in a secretRef
// JSON file. The JSON map key (not a field) holds the API key value itself.
type secretKeyEntry struct {
	TenantID string            `json:"tenantID"`
	APIKeyID string            `json:"apiKeyID,omitempty"`
	Scopes   []string          `json:"scopes,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// loadKeysFromSecretFile reads API keys from a JSON file that maps
// API key value -> tenant context. It works with any file on disk; on
// Kubernetes the file is typically a Secret volume mount. Error messages
// never include key values or raw file content, because the JSON map keys
// are the secrets themselves.
func loadKeysFromSecretFile(path string) (map[string]*TenantContext, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading secretRef file: %w", err)
	}

	var entries map[string]secretKeyEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parsing secretRef file %s: must be a JSON object mapping API key -> {tenantID, apiKeyID, scopes, metadata}", path)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("secretRef file %s contains no API keys", path)
	}

	// Validate in sorted order so errors are deterministic; refer to entries
	// by position, never by name (the name is the API key).
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	slices.Sort(names)

	keys := make(map[string]*TenantContext, len(entries))
	for i, name := range names {
		entry := entries[name]
		if name == "" {
			return nil, fmt.Errorf("secretRef file %s: entry %d of %d: API key must not be empty", path, i+1, len(names))
		}
		if entry.TenantID == "" {
			return nil, fmt.Errorf("secretRef file %s: entry %d of %d: tenantID is required", path, i+1, len(names))
		}
		keys[name] = &TenantContext{
			TenantID: entry.TenantID,
			APIKeyID: entry.APIKeyID,
			Scopes:   entry.Scopes,
			Metadata: entry.Metadata,
		}
	}
	return keys, nil
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
	_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errchkjson // writing to http.ResponseWriter
		"code":    code,
		"message": message,
	})
}
