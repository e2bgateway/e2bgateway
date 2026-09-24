package opensandbox

import (
	"fmt"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/adapter/util"
	"github.com/e2bgateway/e2bgateway/internal/config"
)

// NewAdapterFromConfig creates an OpenSandbox adapter from BackendConfig.
func NewAdapterFromConfig(bcfg config.BackendConfig, registry *adapter.Registry) (adapter.SandboxAdapter, error) {
	cfg := AdapterConfig{
		Name:     bcfg.Name,
		Registry: registry,
	}

	// Required: baseURL
	if v, ok := util.LookupString(bcfg.Config, "baseurl", "baseURL"); ok {
		cfg.BaseURL = v
	} else {
		return nil, fmt.Errorf("baseURL is required for OpenSandbox adapter")
	}

	// Optional string fields
	if v, ok := util.LookupString(bcfg.Config, "apikey", "apiKey"); ok {
		cfg.APIKey = v
	}
	if v, ok := util.LookupString(bcfg.Config, "execdurl", "execdURL"); ok {
		cfg.ExecdURL = v
	}
	if v, ok := util.LookupString(bcfg.Config, "execdtoken", "execdToken"); ok {
		cfg.ExecdToken = v
	}

	// Optional map field
	if m := util.LookupStringMap(bcfg.Config, "templatetoimage", "templateToImage"); m != nil {
		cfg.TemplateToImage = m
	}

	// Optional bool field: dual-mode access token (OSEP-0011 vs random)
	if v, ok := util.LookupBool(bcfg.Config, "usesignedendpoint", "useSignedEndpoint"); ok {
		cfg.UseSignedEndpoint = v
	}

	return New(cfg)
}
