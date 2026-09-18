package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadFromTestConfig(t *testing.T) {
	// Use absolute path based on working directory
	cfgPath := "../../configs/e2bgateway-test.yaml"
	cfg, err := Load(cfgPath)
	if err != nil {
		// Try project-root-relative path
		cfgPath = "configs/e2bgateway-test.yaml"
		cfg, err = Load(cfgPath)
		if err != nil {
			t.Skipf("config file not found at relative paths, skipping: %v", err)
		}
	}
	if cfg.Server.HTTP.Address != "127.0.0.1:18080" {
		t.Errorf("expected address 127.0.0.1:18080, got %s", cfg.Server.HTTP.Address)
	}
	if len(cfg.Backends) != 1 || cfg.Backends[0].Type != "mock" {
		t.Errorf("expected 1 mock backend")
	}
	if cfg.Routing.DefaultBackend != "mock" {
		t.Errorf("expected default backend 'mock', got %s", cfg.Routing.DefaultBackend)
	}
}

func TestJWTConfigValidation(t *testing.T) {
	valid := AuthProviderConfig{
		Type: "jwt", Issuer: "https://issuer.example.test", Audience: "gateway",
		JWKS: `{"keys":[{"kid":"configured-key"}]}`, ClockSkew: 5 * time.Second,
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		change func(*AuthProviderConfig)
	}{
		{"missing issuer", func(c *AuthProviderConfig) { c.Issuer = "" }},
		{"missing audience", func(c *AuthProviderConfig) { c.Audience = "" }},
		{"missing JWKS", func(c *AuthProviderConfig) { c.JWKS = "" }},
		{"URL is PR2 scope", func(c *AuthProviderConfig) { c.JWKSURL = "https://issuer.example.test/jwks" }},
		{"negative skew", func(c *AuthProviderConfig) { c.ClockSkew = -time.Second }},
		{"excess skew", func(c *AuthProviderConfig) { c.ClockSkew = 6 * time.Minute }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.change(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected JWT config validation error")
			}
		})
	}
}

func TestLoadJWTConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jwt.yaml")
	content := `
server:
  http:
    address: "127.0.0.1:0"
backends:
  - name: mock
    type: mock
    enabled: true
auth:
  providers:
    - type: jwt
      issuer: "https://issuer.example.test"
      audience: "gateway"
      jwks: '{"keys":[{"kid":"configured-key"}]}'
      clockSkew: 5s
routing:
  defaultBackend: mock
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Auth.Providers[0]; got.JWKS != `{"keys":[{"kid":"configured-key"}]}` || got.ClockSkew != 5*time.Second {
		t.Fatalf("JWT config mapping: %+v", got)
	}
}

func TestLoadNoConfigFails(t *testing.T) {
	// Loading without a config file should fail because no backends are configured
	_, err := Load("")
	if err == nil {
		t.Fatal("expected error when loading with no config and no backends")
	}
}

func TestLoadFromFile(t *testing.T) {
	// Create a temp config file
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	content := `
server:
  http:
    address: "127.0.0.1:9090"
backends:
  - name: mock
    type: mock
    enabled: true
    config: {}
routing:
  defaultBackend: mock
  strategy: static
observability:
  logging:
    level: debug
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Server.HTTP.Address != "127.0.0.1:9090" {
		t.Errorf("expected address 127.0.0.1:9090, got %s", cfg.Server.HTTP.Address)
	}
	if len(cfg.Backends) != 1 {
		t.Errorf("expected 1 backend, got %d", len(cfg.Backends))
	}
	if cfg.Backends[0].Name != "mock" {
		t.Errorf("expected backend name 'mock', got %s", cfg.Backends[0].Name)
	}
	if cfg.Routing.DefaultBackend != "mock" {
		t.Errorf("expected default backend 'mock', got %s", cfg.Routing.DefaultBackend)
	}
}

func TestValidateNoBackends(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{HTTP: HTTPConfig{Address: "0.0.0.0:8080"}},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for no backends")
	}
}

func TestValidateNoEnabledBackends(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{HTTP: HTTPConfig{Address: "0.0.0.0:8080"}},
		Backends: []BackendConfig{
			{Name: "test", Type: "mock", Enabled: false},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for no enabled backends")
	}
}

func TestValidateAutoSelectDefault(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{HTTP: HTTPConfig{Address: "0.0.0.0:8080"}},
		Backends: []BackendConfig{
			{Name: "mock", Type: "mock", Enabled: true},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error: %v", err)
	}
	if cfg.Routing.DefaultBackend != "mock" {
		t.Errorf("expected auto-selected default 'mock', got %s", cfg.Routing.DefaultBackend)
	}
}
