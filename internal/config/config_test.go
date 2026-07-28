package config

import (
	"os"
	"path/filepath"
	"testing"
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
