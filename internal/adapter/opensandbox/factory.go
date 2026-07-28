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
	// Note: Viper lowercases all map keys, so we check both camelCase and lowercase.
	if baseURL, ok := bcfg.Config["baseurl"].(string); ok {
		cfg.BaseURL = baseURL
	} else if baseURL, ok := bcfg.Config["baseURL"].(string); ok {
		cfg.BaseURL = baseURL
	} else {
		return nil, fmt.Errorf("baseURL is required for OpenSandbox adapter")
	}

	if apiKey, ok := bcfg.Config["apikey"].(string); ok {
		cfg.APIKey = apiKey
	} else if apiKey, ok := bcfg.Config["apiKey"].(string); ok {
		cfg.APIKey = apiKey
	}

	if execdURL, ok := bcfg.Config["execdurl"].(string); ok {
		cfg.ExecdURL = execdURL
	} else if execdURL, ok := bcfg.Config["execdURL"].(string); ok {
		cfg.ExecdURL = execdURL
	}

	if execdToken, ok := bcfg.Config["execdtoken"].(string); ok {
		cfg.ExecdToken = execdToken
	} else if execdToken, ok := bcfg.Config["execdToken"].(string); ok {
		cfg.ExecdToken = execdToken
	}

	// Parse template-to-image mapping
	if tti, ok := bcfg.Config["templatetoimage"].(map[string]interface{}); ok {
		cfg.TemplateToImage = make(map[string]string)
		for k, v := range tti {
			if s, ok := v.(string); ok {
				cfg.TemplateToImage[k] = s
			}
		}
	} else if tti, ok := bcfg.Config["templateToImage"].(map[string]interface{}); ok {
		cfg.TemplateToImage = make(map[string]string)
		for k, v := range tti {
			if s, ok := v.(string); ok {
				cfg.TemplateToImage[k] = s
			}
		}
	}

	return New(cfg)
}
