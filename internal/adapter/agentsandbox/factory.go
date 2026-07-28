package agentsandbox

import (
	"fmt"

	"k8s.io/client-go/rest"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/config"
)

// NewAdapterFromConfig creates an agent-sandbox adapter from BackendConfig.
func NewAdapterFromConfig(bcfg config.BackendConfig) (adapter.SandboxAdapter, error) {
	cfg := AdapterConfig{
		Name: bcfg.Name,
	}

	// Parse config map
	if ns, ok := bcfg.Config["namespace"].(string); ok {
		cfg.Namespace = ns
	}
	if gwName, ok := bcfg.Config["gatewayName"].(string); ok {
		cfg.GatewayName = gwName
	}
	if gwNs, ok := bcfg.Config["gatewayNamespace"].(string); ok {
		cfg.GatewayNamespace = gwNs
	}
	if apiURL, ok := bcfg.Config["apiURL"].(string); ok {
		cfg.APIURL = apiURL
	}

	// Parse template-to-warm-pool mapping
	if t2wp, ok := bcfg.Config["templateToWarmPool"].(map[string]interface{}); ok {
		cfg.TemplateToWarmPool = make(map[string]string)
		for k, v := range t2wp {
			if vs, ok := v.(string); ok {
				cfg.TemplateToWarmPool[k] = vs
			}
		}
	}

	// Try to get rest config from in-cluster or kubeconfig
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		// Fallback: try kubeconfig
		restConfig, err = getRestConfigFromKubeconfig()
		if err != nil {
			return nil, fmt.Errorf("getting rest config: %w", err)
		}
	}
	cfg.RestConfig = restConfig

	return New(cfg)
}

// getRestConfigFromKubeconfig tries to load kubeconfig from default locations.
func getRestConfigFromKubeconfig() (*rest.Config, error) {
	// This is a simplified version. In production, use clientcmd.BuildConfigFromFlags
	// with proper kubeconfig loading logic.
	return nil, fmt.Errorf("kubeconfig loading not implemented; use in-cluster config or provide rest config directly")
}
