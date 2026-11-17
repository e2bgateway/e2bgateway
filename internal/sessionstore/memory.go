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
	"strings"
	"sync"
	"time"
)

// reclaimThreshold is the number of writes between amortized sweeps of
// expired entries. Lazy expiry alone would retain entries for keys never
// read again, growing the map without bound for the process lifetime.
const reclaimThreshold = 1024

type memoryEntry struct {
	value     string
	expiresAt time.Time // zero value = no expiry
}

func (e memoryEntry) expired(now time.Time) bool {
	return !e.expiresAt.IsZero() && now.After(e.expiresAt)
}

// memoryStore is the per-process Store implementation. Expiry is lazy on
// reads, plus an amortized full sweep on writes so unreferenced expired
// entries are reclaimed (bounded memory, like the LRU it replaces).
type memoryStore struct {
	mu               sync.RWMutex
	entries          map[string]memoryEntry
	writesSinceSweep int
}

// NewMemory returns a Store backed by an in-process map. Suitable for
// single-replica deployments and tests.
func NewMemory() Store {
	return &memoryStore{entries: make(map[string]memoryEntry)}
}

func expiry(ttl time.Duration) time.Time {
	if ttl <= 0 {
		return time.Time{}
	}
	return time.Now().Add(ttl)
}

// reclaimExpired drops all expired entries. Caller must hold the write lock.
func (m *memoryStore) reclaimExpired(now time.Time) {
	for k, e := range m.entries {
		if e.expired(now) {
			delete(m.entries, k)
		}
	}
}

// noteWrite records a write and triggers the amortized sweep.
// Caller must hold the write lock.
func (m *memoryStore) noteWrite(now time.Time) {
	m.writesSinceSweep++
	if m.writesSinceSweep >= reclaimThreshold {
		m.reclaimExpired(now)
		m.writesSinceSweep = 0
	}
}

func (m *memoryStore) Get(_ context.Context, key string) (string, bool, error) {
	m.mu.RLock()
	e, ok := m.entries[key]
	m.mu.RUnlock()
	if !ok || e.expired(time.Now()) {
		return "", false, nil
	}
	return e.value, true, nil
}

func (m *memoryStore) Set(_ context.Context, key, value string, ttl time.Duration) error {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = memoryEntry{value: value, expiresAt: expiry(ttl)}
	m.noteWrite(now)
	return nil
}

func (m *memoryStore) SetNX(_ context.Context, key, value string, ttl time.Duration) (bool, error) {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[key]; ok && !e.expired(now) {
		return false, nil
	}
	m.entries[key] = memoryEntry{value: value, expiresAt: expiry(ttl)}
	m.noteWrite(now)
	return true, nil
}

func (m *memoryStore) Touch(_ context.Context, key string, ttl time.Duration) error {
	if ttl <= 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Re-read the clock AFTER acquiring the lock: an entry that expired
	// while we waited must stay expired, not get a fresh lifetime.
	now := time.Now()
	e, ok := m.entries[key]
	if !ok || e.expired(now) {
		return nil // missing or expired: nothing to refresh, never resurrect
	}
	e.expiresAt = expiry(ttl)
	m.entries[key] = e
	return nil
}

func (m *memoryStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, key)
	return nil
}

func (m *memoryStore) Scan(_ context.Context, prefix string, visit func(key, value string)) error {
	now := time.Now()
	m.mu.RLock()
	defer m.mu.RUnlock()
	for k, e := range m.entries {
		if strings.HasPrefix(k, prefix) && !e.expired(now) {
			visit(k, e.value)
		}
	}
	return nil
}
