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
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryStoreContract(t *testing.T) {
	runStoreContract(t, storeFixture{
		store:   NewMemory(),
		advance: func(t *testing.T, d time.Duration) { time.Sleep(d) },
	})
}

// TestMemoryStoreReclaimsExpiredEntries verifies the amortized sweep bounds
// the map: expired entries for keys never read again must be reclaimed
// (issue: lazy expiry alone retains them for the process lifetime).
func TestMemoryStoreReclaimsExpiredEntries(t *testing.T) {
	ctx := context.Background()
	m := NewMemory().(*memoryStore)

	// Seed entries that expire almost immediately.
	for i := range 50 {
		require.NoError(t, m.Set(ctx, "expired:"+strconv.Itoa(i), "v", time.Millisecond))
	}
	time.Sleep(10 * time.Millisecond)

	// One more write triggers the amortized sweep deterministically when the
	// threshold is reached; force the counter so the test stays fast.
	m.mu.Lock()
	m.writesSinceSweep = reclaimThreshold - 1
	m.mu.Unlock()
	require.NoError(t, m.Set(ctx, "live", "v", time.Hour))

	m.mu.RLock()
	n := len(m.entries)
	_, liveOK := m.entries["live"]
	m.mu.RUnlock()
	assert.Equal(t, 1, n, "expired entries must be reclaimed by the sweep")
	assert.True(t, liveOK)
}
