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

package agentsandbox

import (
	"fmt"

	"k8s.io/client-go/rest"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/adapter/util"
	"github.com/e2bgateway/e2bgateway/internal/config"
)

// NewAdapterFromConfig creates an agent-sandbox adapter from BackendConfig.
func NewAdapterFromConfig(bcfg config.BackendConfig, registry *adapter.Registry) (adapter.SandboxAdapter, error) {
	cfg := AdapterConfig{Name: bcfg.Name, Registry: registry}
	parseBackendConfig(bcfg.Config, &cfg)

	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("getting rest config (in-cluster only): %w", err)
	}
	cfg.RestConfig = restConfig

	return New(cfg)
}

// parseBackendConfig extracts adapter config values from the raw backend config map.
func parseBackendConfig(raw map[string]any, cfg *AdapterConfig) {
	cfg.Namespace = util.StringVal(raw, "namespace")
	cfg.GatewayName = util.StringVal(raw, "gatewayname", "gatewayName")
	cfg.GatewayNamespace = util.StringVal(raw, "gatewaynamespace", "gatewayNamespace")
	cfg.APIURL = util.StringVal(raw, "apiurl", "apiURL")
	cfg.WarmPoolName = util.StringVal(raw, "warmpoolname", "warmPoolName")

	// UseEnvdDataPlane defaults to true for agent-sandbox backend
	cfg.UseEnvdDataPlane = boolVal(raw, true, "useenvddataplane", "useEnvdDataPlane")

	if t2wp, ok := mapVal(raw, "templatetowarmpool", "templateToWarmPool"); ok {
		cfg.TemplateToWarmPool = t2wp
	}
}

// mapVal returns a map[string]string from the first matching key in m.
func mapVal(m map[string]any, keys ...string) (map[string]string, bool) {
	for _, k := range keys {
		if raw, ok := m[k].(map[string]any); ok {
			result := make(map[string]string, len(raw))
			for mk, mv := range raw {
				if s, ok := mv.(string); ok {
					result[mk] = s
				}
			}
			return result, true
		}
	}
	return nil, false
}

// boolVal returns a bool from the first matching key in m, or defaultValue if not found.
func boolVal(m map[string]any, defaultValue bool, keys ...string) bool {
	for _, k := range keys {
		if v, ok := m[k].(bool); ok {
			return v
		}
	}
	return defaultValue
}
