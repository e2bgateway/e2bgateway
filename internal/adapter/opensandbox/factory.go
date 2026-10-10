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

package opensandbox

import (
	"context"
	"fmt"
	"time"

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

	// Optional templateStore configuration.
	if storeCfg, ok := lookupMap(bcfg.Config, "templatestore", "templateStore"); ok {
		store, err := buildTemplateStore(storeCfg)
		if err != nil {
			return nil, fmt.Errorf("templateStore: %w", err)
		}
		cfg.TemplateStore = store
	}

	return New(cfg)
}

// buildTemplateStore creates a TemplateStore from the store config map.
// Supported types: "memory" (default), "redis".
func buildTemplateStore(storeCfg map[string]any) (TemplateStore, error) {
	storeType, _ := lookupString(storeCfg, "type")
	if storeType == "" {
		storeType = "memory"
	}

	switch storeType {
	case "memory":
		return NewMemoryTemplateStore(), nil
	case "redis":
		return buildRedisStore(storeCfg)
	default:
		return nil, fmt.Errorf("unknown templateStore type %q (supported: memory, redis)", storeType)
	}
}

// buildRedisStore creates a RedisTemplateStore from config.
func buildRedisStore(storeCfg map[string]any) (*RedisTemplateStore, error) {
	redisCfg := RedisConfig{}
	if v, ok := lookupString(storeCfg, "addr"); ok {
		redisCfg.Addr = v
	} else if v, ok := lookupString(storeCfg, "address"); ok {
		redisCfg.Addr = v
	} else {
		return nil, fmt.Errorf("redis addr is required")
	}
	if v, ok := lookupString(storeCfg, "password"); ok {
		redisCfg.Password = v
	}
	if v, ok := lookupInt(storeCfg, "db"); ok {
		redisCfg.DB = v
	}
	if v, ok := lookupString(storeCfg, "keyprefix", "keyPrefix"); ok {
		redisCfg.KeyPrefix = v
	}
	if v, ok := lookupDuration(storeCfg, "dialtimeout", "dialTimeout"); ok {
		redisCfg.DialTimeout = v
	}
	if v, ok := lookupDuration(storeCfg, "readtimeout", "readTimeout"); ok {
		redisCfg.ReadTimeout = v
	}
	if v, ok := lookupDuration(storeCfg, "writetimeout", "writeTimeout"); ok {
		redisCfg.WriteTimeout = v
	}

	return NewRedisTemplateStore(context.Background(), redisCfg)
}

// --- Config lookup helpers (local to this package) ---

// lookupString returns the first string value found for the given keys.
func lookupString(m map[string]any, keys ...string) (string, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s, true
			}
		}
	}
	return "", false
}

// lookupMap returns the first map value found for the given keys.
func lookupMap(m map[string]any, keys ...string) (map[string]any, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if sub, ok := v.(map[string]any); ok {
				return sub, true
			}
		}
	}
	return nil, false
}

// lookupInt returns the first integer value found for the given key.
func lookupInt(m map[string]any, key string) (int, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

// lookupDuration returns the first duration value found for the given keys.
func lookupDuration(m map[string]any, keys ...string) (time.Duration, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch d := v.(type) {
			case string:
				if parsed, err := time.ParseDuration(d); err == nil {
					return parsed, true
				}
			case int:
				return time.Duration(d) * time.Second, true
			case int64:
				return time.Duration(d) * time.Second, true
			case float64:
				return time.Duration(d * float64(time.Second)), true
			}
		}
	}
	return 0, false
}
