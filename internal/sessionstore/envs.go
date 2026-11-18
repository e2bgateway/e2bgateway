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

package sessionstore

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"
)

// EnvsKey returns the store key holding a sandbox's environment variables.
func EnvsKey(sandboxID string) string { return "envs:" + sandboxID }

// GetEnvs reads the environment variables stored for a sandbox. A missing
// key yields an empty map, not an error. A successful read refreshes the TTL
// (pass ttl <= 0 to skip) so the variables live as long as the sandbox keeps
// serving traffic, matching the routing-entry convention.
func GetEnvs(ctx context.Context, s Store, sandboxID string, ttl time.Duration) (map[string]string, error) {
	raw, ok, err := s.Get(ctx, EnvsKey(sandboxID))
	if err != nil {
		return nil, err
	}
	if !ok {
		return map[string]string{}, nil
	}
	envs := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &envs); err != nil {
		return nil, fmt.Errorf("decoding envs for sandbox %q: %w", sandboxID, err)
	}
	if ttl > 0 {
		// Best-effort: a failed refresh must not fail the read.
		_ = s.Touch(ctx, EnvsKey(sandboxID), ttl)
	}
	return envs, nil
}

// MergeEnvs merges envs into the sandbox's stored set, creating it when
// absent. Merges racing across replicas can drop variables (read-modify-write);
// callers tolerate this because SetEnvs is infrequent and create-time envs are
// set before the sandbox is announced.
func MergeEnvs(ctx context.Context, s Store, sandboxID string, envs map[string]string, ttl time.Duration) error {
	if len(envs) == 0 {
		return nil
	}
	merged, err := GetEnvs(ctx, s, sandboxID, 0)
	if err != nil {
		return err
	}
	maps.Copy(merged, envs)
	raw, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("encoding envs for sandbox %q: %w", sandboxID, err)
	}
	return s.Set(ctx, EnvsKey(sandboxID), string(raw), ttl)
}
