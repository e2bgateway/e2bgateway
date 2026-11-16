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

type memoryEntry struct {
	value     string
	expiresAt time.Time // zero value = no expiry
}

func (e memoryEntry) expired(now time.Time) bool {
	return !e.expiresAt.IsZero() && now.After(e.expiresAt)
}

// memoryStore is the per-process Store implementation. Expiry is lazy:
// entries are dropped when touched by Get/SetNX/Delete/Scan.
type memoryStore struct {
	mu      sync.RWMutex
	entries map[string]memoryEntry
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
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = memoryEntry{value: value, expiresAt: expiry(ttl)}
	return nil
}

func (m *memoryStore) SetNX(_ context.Context, key, value string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[key]; ok && !e.expired(time.Now()) {
		return false, nil
	}
	m.entries[key] = memoryEntry{value: value, expiresAt: expiry(ttl)}
	return true, nil
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
