package opensandbox

import (
	"fmt"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/config"
)

// NewAdapterFromConfig creates an OpenSandbox adapter from BackendConfig.
func NewAdapterFromConfig(bcfg config.BackendConfig) (adapter.SandboxAdapter, error) {
	cfg := AdapterConfig{
		Name: bcfg.Name,
	}

	// Parse config map
	if baseURL, ok := bcfg.Config["baseURL"].(string); ok {
		cfg.BaseURL = baseURL
	} else {
		return nil, fmt.Errorf("baseURL is required for OpenSandbox adapter")
	}

	if apiKey, ok := bcfg.Config["apiKey"].(string); ok {
		cfg.APIKey = apiKey
	}

	if execdURL, ok := bcfg.Config["execdURL"].(string); ok {
		cfg.ExecdURL = execdURL
	}

	if execdToken, ok := bcfg.Config["execdToken"].(string); ok {
		cfg.ExecdToken = execdToken
	}

	return New(cfg)
}
