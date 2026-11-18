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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvsMergeAndGet(t *testing.T) {
	s := NewMemory()
	ctx := t.Context()

	envs, err := GetEnvs(ctx, s, "sb-1", 0)
	require.NoError(t, err)
	assert.Empty(t, envs)

	require.NoError(t, MergeEnvs(ctx, s, "sb-1", map[string]string{"A": "1"}, time.Hour))
	require.NoError(t, MergeEnvs(ctx, s, "sb-1", map[string]string{"B": "2"}, time.Hour))
	require.NoError(t, MergeEnvs(ctx, s, "sb-1", nil, time.Hour))

	envs, err = GetEnvs(ctx, s, "sb-1", time.Hour)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"A": "1", "B": "2"}, envs)

	// Keys are per-sandbox.
	envs, err = GetEnvs(ctx, s, "sb-2", 0)
	require.NoError(t, err)
	assert.Empty(t, envs)

	require.NoError(t, s.Delete(ctx, EnvsKey("sb-1")))
	envs, err = GetEnvs(ctx, s, "sb-1", 0)
	require.NoError(t, err)
	assert.Empty(t, envs)
}
