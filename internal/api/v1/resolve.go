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
