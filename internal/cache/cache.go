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

package cache

import (
	"container/list"
	"sync"
	"time"
)

// Cache is a thread-safe in-memory LRU cache with TTL.
type Cache struct {
	mu         sync.RWMutex
	maxSize    int
	defaultTTL time.Duration
	items      map[string]*list.Element
	order      *list.List
}

type entry struct {
	key       string
	value     any
	expiresAt time.Time
}

// New creates a new Cache with the given max size and default TTL.
func New(maxSize int, defaultTTL time.Duration) *Cache {
	if maxSize <= 0 {
		maxSize = 1000
	}
	if defaultTTL <= 0 {
		defaultTTL = 30 * time.Second
	}
	return &Cache{
		maxSize:    maxSize,
		defaultTTL: defaultTTL,
		items:      make(map[string]*list.Element),
		order:      list.New(),
	}
}

// Get retrieves a value from the cache. Returns nil, false if not found or expired.
func (c *Cache) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.items[key]
	if !ok {
		return nil, false
	}

	e := getEntry(elem)
	if time.Now().After(e.expiresAt) {
		c.removeLocked(elem)
		return nil, false
	}

	c.order.MoveToFront(elem)
	return e.value, true
}

// Set adds or updates a value in the cache with the default TTL.
func (c *Cache) Set(key string, value any) {
	c.SetWithTTL(key, value, c.defaultTTL)
}

// SetWithTTL adds or updates a value with a custom TTL.
func (c *Cache) SetWithTTL(key string, value any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.items[key]; ok {
		c.order.MoveToFront(elem)
		e := getEntry(elem)
		e.value = value
		e.expiresAt = time.Now().Add(ttl)
		return
	}

	e := &entry{
		key:       key,
		value:     value,
		expiresAt: time.Now().Add(ttl),
	}
	elem := c.order.PushFront(e)
	c.items[key] = elem

	// Evict oldest if over capacity
	for c.order.Len() > c.maxSize {
		c.removeLocked(c.order.Back())
	}
}

// Delete removes a key from the cache.
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.items[key]; ok {
		c.removeLocked(elem)
	}
}

// Len returns the number of items in the cache (including expired).
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.order.Len()
}

// Cleanup removes all expired entries.
func (c *Cache) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for e := c.order.Back(); e != nil; {
		ent := getEntry(e)
		prev := e.Prev()
		if now.After(ent.expiresAt) {
			c.removeLocked(e)
		}
		e = prev
	}
}

func (c *Cache) removeLocked(elem *list.Element) {
	e := getEntry(elem)
	delete(c.items, e.key)
	c.order.Remove(elem)
}

// getEntry extracts the *entry from a list.Element.
// Panics if the element does not contain an *entry (should never happen).
func getEntry(elem *list.Element) *entry {
	return elem.Value.(*entry) //nolint:errcheck // we control what is stored
}
