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

package opensandbox

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
)

// Compile-time assertion that MemoryTemplateStore implements TemplateStore.
var _ TemplateStore = (*MemoryTemplateStore)(nil)

// MemoryTemplateStore is an in-process TemplateStore backed by Go maps.
// Suitable for development, testing, and single-instance deployments where
// restart-time data loss is acceptable. All state is lost when the process
// exits.
//
// Concurrent access is safe: all operations are guarded by a single RWMutex.
type MemoryTemplateStore struct {
	mu sync.RWMutex

	templates      map[string]*TemplateEntry
	builds         map[string]*adapter.BuildStatus // buildID → build
	templateBuilds map[string]map[string]struct{}  // templateID → set<buildID>
	aliases        map[string][]string             // templateID → aliases
	aliasReverse   map[string]string               // alias → templateID
	tags           map[string][]*adapter.Tag       // templateID → tags
}

// NewMemoryTemplateStore creates an empty in-memory template store.
func NewMemoryTemplateStore() *MemoryTemplateStore {
	return &MemoryTemplateStore{
		templates:      make(map[string]*TemplateEntry),
		builds:         make(map[string]*adapter.BuildStatus),
		templateBuilds: make(map[string]map[string]struct{}),
		aliases:        make(map[string][]string),
		aliasReverse:   make(map[string]string),
		tags:           make(map[string][]*adapter.Tag),
	}
}

// --- Templates ---

func (s *MemoryTemplateStore) CreateTemplate(_ context.Context, entry *TemplateEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.templates[entry.TemplateID]; exists {
		return fmt.Errorf("template %q: %w", entry.TemplateID, ErrTemplateExists)
	}
	// Store a copy so the caller can't mutate our internal state.
	cp := *entry
	if entry.Metadata != nil {
		cp.Metadata = make(map[string]string, len(entry.Metadata))
		maps.Copy(cp.Metadata, entry.Metadata)
	}
	s.templates[entry.TemplateID] = &cp
	return nil
}

func (s *MemoryTemplateStore) GetTemplate(_ context.Context, templateID string) (*TemplateEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.templates[templateID]
	if !ok {
		return nil, fmt.Errorf("template %q: %w", templateID, ErrTemplateNotFound)
	}
	cp := *entry
	return &cp, nil
}

func (s *MemoryTemplateStore) ListTemplates(_ context.Context) ([]*TemplateEntry, error) {
	s.mu.RLock()
	entries := make([]*TemplateEntry, 0, len(s.templates))
	for _, e := range s.templates {
		cp := *e
		entries = append(entries, &cp)
	}
	s.mu.RUnlock()

	// Stable order by creation time.
	slices.SortFunc(entries, func(a, b *TemplateEntry) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return entries, nil
}

func (s *MemoryTemplateStore) UpdateTemplate(_ context.Context, templateID string, update func(*TemplateEntry) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.templates[templateID]
	if !ok {
		return fmt.Errorf("template %q: %w", templateID, ErrTemplateNotFound)
	}
	// Pass a copy to the callback to prevent races if the caller retains
	// the pointer beyond the lock scope.
	cp := *entry
	if err := update(&cp); err != nil {
		return err
	}
	s.templates[templateID] = &cp
	return nil
}

func (s *MemoryTemplateStore) DeleteTemplate(_ context.Context, templateID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.templates[templateID]; !ok {
		return fmt.Errorf("template %q: %w", templateID, ErrTemplateNotFound)
	}
	delete(s.templates, templateID)
	return nil
}

// --- Builds ---

func (s *MemoryTemplateStore) SaveBuild(_ context.Context, templateID string, build *adapter.BuildStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cp := *build
	s.builds[build.BuildID] = &cp
	if s.templateBuilds[templateID] == nil {
		s.templateBuilds[templateID] = make(map[string]struct{})
	}
	s.templateBuilds[templateID][build.BuildID] = struct{}{}
	return nil
}

func (s *MemoryTemplateStore) GetBuild(_ context.Context, buildID string) (*adapter.BuildStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	bs, ok := s.builds[buildID]
	if !ok {
		return nil, fmt.Errorf("build %q: %w", buildID, ErrBuildNotFound)
	}
	cp := *bs
	return &cp, nil
}

func (s *MemoryTemplateStore) ListBuilds(_ context.Context, templateID string) ([]*adapter.BuildStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	buildIDs := s.templateBuilds[templateID]
	result := make([]*adapter.BuildStatus, 0, len(buildIDs))
	for id := range buildIDs {
		if bs, ok := s.builds[id]; ok {
			cp := *bs
			result = append(result, &cp)
		}
	}
	return result, nil
}

func (s *MemoryTemplateStore) DeleteBuilds(_ context.Context, templateID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for buildID := range s.templateBuilds[templateID] {
		delete(s.builds, buildID)
	}
	delete(s.templateBuilds, templateID)
	return nil
}

// --- Aliases ---

func (s *MemoryTemplateStore) AddAlias(_ context.Context, templateID, alias string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	aliases := s.aliases[templateID]
	if slices.Contains(aliases, alias) {
		return nil // idempotent
	}
	s.aliases[templateID] = append(aliases, alias)
	s.aliasReverse[alias] = templateID
	return nil
}

func (s *MemoryTemplateStore) RemoveAlias(_ context.Context, templateID, alias string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	aliases := s.aliases[templateID]
	idx := slices.Index(aliases, alias)
	if idx < 0 {
		return fmt.Errorf("alias %q for template %q: %w", alias, templateID, ErrAliasNotFound)
	}
	s.aliases[templateID] = append(aliases[:idx], aliases[idx+1:]...)
	delete(s.aliasReverse, alias)
	return nil
}

func (s *MemoryTemplateStore) ListAliases(_ context.Context, templateID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	aliases := s.aliases[templateID]
	result := make([]string, len(aliases))
	copy(result, aliases)
	return result, nil
}

func (s *MemoryTemplateStore) ResolveAlias(_ context.Context, alias string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	templateID, ok := s.aliasReverse[alias]
	if !ok {
		return "", fmt.Errorf("alias %q: %w", alias, ErrAliasNotFound)
	}
	return templateID, nil
}

func (s *MemoryTemplateStore) DeleteAliases(_ context.Context, templateID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, alias := range s.aliases[templateID] {
		delete(s.aliasReverse, alias)
	}
	delete(s.aliases, templateID)
	return nil
}

// --- Tags ---

func (s *MemoryTemplateStore) SaveTag(_ context.Context, templateID string, tag *adapter.Tag) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tags := s.tags[templateID]
	cp := *tag
	for i, t := range tags {
		if t.Name == tag.Name {
			tags[i] = &cp
			return nil
		}
	}
	s.tags[templateID] = append(tags, &cp)
	return nil
}

func (s *MemoryTemplateStore) ListTags(_ context.Context, templateID string) ([]*adapter.Tag, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tags := s.tags[templateID]
	result := make([]*adapter.Tag, len(tags))
	for i, t := range tags {
		cp := *t
		result[i] = &cp
	}
	return result, nil
}

func (s *MemoryTemplateStore) DeleteTag(_ context.Context, templateID, tagName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tags := s.tags[templateID]
	idx := -1
	for i, t := range tags {
		if t.Name == tagName {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("tag %q for template %q: %w", tagName, templateID, ErrTagNotFound)
	}
	s.tags[templateID] = append(tags[:idx], tags[idx+1:]...)
	return nil
}

func (s *MemoryTemplateStore) DeleteTags(_ context.Context, templateID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.tags, templateID)
	return nil
}

// --- Lifecycle ---

func (s *MemoryTemplateStore) Close() error { return nil }
