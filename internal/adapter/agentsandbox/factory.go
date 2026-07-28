package agentsandbox

import (
	"fmt"

	"k8s.io/client-go/rest"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/config"
)

// NewAdapterFromConfig creates an agent-sandbox adapter from BackendConfig.
func NewAdapterFromConfig(bcfg config.BackendConfig) (adapter.SandboxAdapter, error) {
	cfg := AdapterConfig{Name: bcfg.Name}
	parseBackendConfig(bcfg.Config, &cfg)

	restConfig, err := rest.InClusterConfig()
	if err != nil {
		restConfig, err = getRestConfigFromKubeconfig()
		if err != nil {
			return nil, fmt.Errorf("getting rest config: %w", err)
		}
	}
	cfg.RestConfig = restConfig

	return New(cfg)
}

// parseBackendConfig extracts adapter config values from the raw backend config map.
func parseBackendConfig(raw map[string]interface{}, cfg *AdapterConfig) {
	cfg.Namespace = stringVal(raw, "namespace")
	cfg.GatewayName = stringVal(raw, "gatewayname", "gatewayName")
	cfg.GatewayNamespace = stringVal(raw, "gatewaynamespace", "gatewayNamespace")
	cfg.APIURL = stringVal(raw, "apiurl", "apiURL")
	cfg.WarmPoolName = stringVal(raw, "warmpoolname", "warmPoolName")

	if t2wp, ok := mapVal(raw, "templatetowarmpool", "templateToWarmPool"); ok {
		cfg.TemplateToWarmPool = t2wp
	}
}

// stringVal returns the first matching key found in m (case-insensitive variants).
func stringVal(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok {
			return v
		}
	}
	return ""
}

// mapVal returns a map[string]string from the first matching key in m.
func mapVal(m map[string]interface{}, keys ...string) (map[string]string, bool) {
	for _, k := range keys {
		if raw, ok := m[k].(map[string]interface{}); ok {
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

// getRestConfigFromKubeconfig tries to load kubeconfig from default locations.
func getRestConfigFromKubeconfig() (*rest.Config, error) {
	return nil, fmt.Errorf("kubeconfig loading not implemented; use in-cluster config or provide rest config")
}
