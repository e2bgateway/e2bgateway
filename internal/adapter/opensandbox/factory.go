package opensandbox

import (
	"fmt"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/config"
)

// lookupString returns the first string value found for any of the given keys
// in the config map. Viper lowercases all keys, so callers typically pass both
// camelCase and lowercase variants.
func lookupString(cfg map[string]interface{}, keys ...string) (string, bool) {
	for _, k := range keys {
		if v, ok := cfg[k].(string); ok {
			return v, true
		}
	}
	return "", false
}

// lookupBool returns the first bool value found for any of the given keys.
func lookupBool(cfg map[string]interface{}, keys ...string) (bool, bool) {
	for _, k := range keys {
		if v, ok := cfg[k].(bool); ok {
			return v, true
		}
	}
	return false, false
}

// lookupStringMap returns a map[string]string by coercing any string values
// found under any of the given keys.
func lookupStringMap(cfg map[string]interface{}, keys ...string) map[string]string {
	for _, k := range keys {
		if raw, ok := cfg[k].(map[string]interface{}); ok {
			out := make(map[string]string, len(raw))
			for mk, mv := range raw {
				if s, ok := mv.(string); ok {
					out[mk] = s
				}
			}
			return out
		}
	}
	return nil
}

// NewAdapterFromConfig creates an OpenSandbox adapter from BackendConfig.
func NewAdapterFromConfig(bcfg config.BackendConfig) (adapter.SandboxAdapter, error) {
	cfg := AdapterConfig{
		Name: bcfg.Name,
	}

	// Required: baseURL
	if v, ok := lookupString(bcfg.Config, "baseurl", "baseURL"); ok {
		cfg.BaseURL = v
	} else {
		return nil, fmt.Errorf("baseURL is required for OpenSandbox adapter")
	}

	// Optional string fields
	if v, ok := lookupString(bcfg.Config, "apikey", "apiKey"); ok {
		cfg.APIKey = v
	}
	if v, ok := lookupString(bcfg.Config, "execdurl", "execdURL"); ok {
		cfg.ExecdURL = v
	}
	if v, ok := lookupString(bcfg.Config, "execdtoken", "execdToken"); ok {
		cfg.ExecdToken = v
	}

	// Optional map field
	if m := lookupStringMap(bcfg.Config, "templatetoimage", "templateToImage"); m != nil {
		cfg.TemplateToImage = m
	}

	// Optional bool field: dual-mode access token (OSEP-0011 vs random)
	if v, ok := lookupBool(bcfg.Config, "usesignedendpoint", "useSignedEndpoint"); ok {
		cfg.UseSignedEndpoint = v
	}

	return New(cfg)
}
