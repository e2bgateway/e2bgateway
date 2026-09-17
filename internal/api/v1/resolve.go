// Package v1 provides HTTP handlers for the E2BGateway API.
package v1

import (
	"context"
	"fmt"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
)

// resolveAdapterForSandbox finds the adapter that owns a specific sandbox.
// It first tries the sandbox→backend mapping for efficient lookup,
// then falls back to iterating all adapters if the mapping misses.
func resolveAdapterForSandbox(registry *adapter.Registry, sandboxID string) (adapter.SandboxAdapter, error) {
	// First try: lookup from sandbox→backend mapping
	if registry.SandboxBackend() != nil {
		if backendName, ok := registry.SandboxBackend().Get(sandboxID); ok {
			if a, ok := registry.Get(backendName); ok {
				return a, nil
			}
		}
	}

	// Fallback: iterate all adapters (backward compatibility)
	for _, a := range registry.List() {
		_, err := a.GetSandbox(context.Background(), sandboxID)
		if err == nil {
			return a, nil
		}
	}

	return nil, fmt.Errorf("sandbox %q not found", sandboxID)
}
