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
	"errors"
	"fmt"
	"net/http"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
)

// resolveAdapterForSandbox finds the adapter that owns a specific sandbox.
// It first tries the sandbox→backend mapping for efficient lookup,
// then falls back to iterating all adapters if the mapping misses.
func resolveAdapterForSandbox(ctx context.Context, registry *adapter.Registry, sandboxID string) (adapter.SandboxAdapter, error) {
	var lookupErr error
	// First try: lookup from sandbox→backend mapping. A store error falls
	// through to the (slower) iterate-all-adapters fallback below.
	if registry.SandboxBackend() != nil {
		if backendName, ok, err := registry.SandboxBackend().Get(ctx, sandboxID); err != nil {
			lookupErr = fmt.Errorf("reading sandbox routing: %w", err)
		} else if ok {
			if a, ok := registry.Get(backendName); ok {
				return a, nil
			}
		}
	}

	// Fallback: iterate all adapters (backward compatibility)
	for _, a := range registry.List() {
		_, err := a.GetSandbox(ctx, sandboxID)
		if err == nil {
			return a, nil
		}
		if !isSandboxNotFound(err) {
			lookupErr = errors.Join(lookupErr, fmt.Errorf("probing backend %q: %w", a.Name(), err))
		}
	}

	if lookupErr != nil {
		return nil, lookupErr
	}
	return nil, sandboxNotFoundError{sandboxID: sandboxID}
}

type sandboxNotFoundError struct {
	sandboxID string
}

func (e sandboxNotFoundError) Error() string {
	return fmt.Sprintf("sandbox %q not found", e.sandboxID)
}

func (sandboxNotFoundError) HTTPStatusCode() int { return http.StatusNotFound }

func isSandboxNotFound(err error) bool {
	if _, ok := status.FromError(err); ok {
		return status.Code(err) == codes.NotFound
	}
	var statusErr httpStatusError
	if errors.As(err, &statusErr) {
		return statusErr.HTTPStatusCode() == http.StatusNotFound
	}
	// Older adapters report missing sandboxes as plain text errors.
	return isNotFoundStatus(err) || strings.Contains(strings.ToLower(err.Error()), "not found")
}
