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
	"errors"
	"testing"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
)

// templateStoreContract runs a battery of tests against any TemplateStore
// implementation. Used by both Memory and Redis tests to ensure behavioral
// parity.
func templateStoreContract(t *testing.T, newStore func(t *testing.T) TemplateStore) {
	t.Helper()
	ctx := context.Background()

	t.Run("Templates_CRUD", func(t *testing.T) {
		s := newStore(t)
		defer s.Close()

		entry := &TemplateEntry{
			TemplateID: "tpl-1",
			Name:       "test-template",
			ImageURI:   "python:3.11-slim",
			CPUCount:   2,
			MemoryMB:   1024,
			BuildID:    "build-1",
			CreatedAt:  time.Now().Truncate(time.Millisecond),
		}

		// Create.
		if err := s.CreateTemplate(ctx, entry); err != nil {
			t.Fatalf("CreateTemplate: %v", err)
		}

		// Duplicate create.
		if err := s.CreateTemplate(ctx, entry); !errors.Is(err, ErrTemplateExists) {
			t.Errorf("duplicate CreateTemplate: got %v, want ErrTemplateExists", err)
		}

		// Get.
		got, err := s.GetTemplate(ctx, "tpl-1")
		if err != nil {
			t.Fatalf("GetTemplate: %v", err)
		}
		if got.Name != "test-template" || got.ImageURI != "python:3.11-slim" {
			t.Errorf("GetTemplate = %+v, want Name=test-template, ImageURI=python:3.11-slim", got)
		}

		// List.
		list, err := s.ListTemplates(ctx)
		if err != nil {
			t.Fatalf("ListTemplates: %v", err)
		}
		if len(list) != 1 || list[0].TemplateID != "tpl-1" {
			t.Errorf("ListTemplates = %+v, want [tpl-1]", list)
		}

		// Update.
		err = s.UpdateTemplate(ctx, "tpl-1", func(e *TemplateEntry) error {
			e.CPUCount = 4
			return nil
		})
		if err != nil {
			t.Fatalf("UpdateTemplate: %v", err)
		}
		updated, _ := s.GetTemplate(ctx, "tpl-1")
		if updated.CPUCount != 4 {
			t.Errorf("UpdateTemplate: CPUCount = %d, want 4", updated.CPUCount)
		}

		// Delete.
		if err := s.DeleteTemplate(ctx, "tpl-1"); err != nil {
			t.Fatalf("DeleteTemplate: %v", err)
		}
		if _, err := s.GetTemplate(ctx, "tpl-1"); !errors.Is(err, ErrTemplateNotFound) {
			t.Errorf("after delete, GetTemplate: got %v, want ErrTemplateNotFound", err)
		}
		// Delete non-existent.
		if err := s.DeleteTemplate(ctx, "tpl-1"); !errors.Is(err, ErrTemplateNotFound) {
			t.Errorf("delete non-existent: got %v, want ErrTemplateNotFound", err)
		}
	})

	t.Run("Templates_UpdateNotFound", func(t *testing.T) {
		s := newStore(t)
		defer s.Close()

		err := s.UpdateTemplate(ctx, "nonexistent", func(e *TemplateEntry) error { return nil })
		if !errors.Is(err, ErrTemplateNotFound) {
			t.Errorf("UpdateTemplate non-existent: got %v, want ErrTemplateNotFound", err)
		}
	})

	t.Run("Builds", func(t *testing.T) {
		s := newStore(t)
		defer s.Close()

		_ = s.CreateTemplate(ctx, &TemplateEntry{
			TemplateID: "tpl-1", Name: "t1", CreatedAt: time.Now(),
		})

		// Save build.
		build := &adapter.BuildStatus{BuildID: "build-1", Status: "ready"}
		if err := s.SaveBuild(ctx, "tpl-1", build); err != nil {
			t.Fatalf("SaveBuild: %v", err)
		}

		// Get.
		got, err := s.GetBuild(ctx, "build-1")
		if err != nil {
			t.Fatalf("GetBuild: %v", err)
		}
		if got.Status != "ready" {
			t.Errorf("GetBuild.Status = %q, want 'ready'", got.Status)
		}

		// List.
		list, err := s.ListBuilds(ctx, "tpl-1")
		if err != nil {
			t.Fatalf("ListBuilds: %v", err)
		}
		if len(list) != 1 {
			t.Errorf("ListBuilds returned %d, want 1", len(list))
		}

		// Delete.
		if err := s.DeleteBuilds(ctx, "tpl-1"); err != nil {
			t.Fatalf("DeleteBuilds: %v", err)
		}
		if _, err := s.GetBuild(ctx, "build-1"); !errors.Is(err, ErrBuildNotFound) {
			t.Errorf("after DeleteBuilds, GetBuild: got %v, want ErrBuildNotFound", err)
		}

		// Get non-existent.
		if _, err := s.GetBuild(ctx, "no-such"); !errors.Is(err, ErrBuildNotFound) {
			t.Errorf("GetBuild non-existent: got %v, want ErrBuildNotFound", err)
		}
	})

	t.Run("Aliases", func(t *testing.T) {
		s := newStore(t)
		defer s.Close()

		_ = s.CreateTemplate(ctx, &TemplateEntry{
			TemplateID: "tpl-1", Name: "t1", CreatedAt: time.Now(),
		})

		// Add.
		if err := s.AddAlias(ctx, "tpl-1", "latest"); err != nil {
			t.Fatalf("AddAlias: %v", err)
		}
		// Idempotent.
		if err := s.AddAlias(ctx, "tpl-1", "latest"); err != nil {
			t.Errorf("AddAlias idempotent: %v", err)
		}
		// Second alias.
		if err := s.AddAlias(ctx, "tpl-1", "stable"); err != nil {
			t.Fatalf("AddAlias stable: %v", err)
		}

		// Resolve.
		id, err := s.ResolveAlias(ctx, "latest")
		if err != nil || id != "tpl-1" {
			t.Errorf("ResolveAlias = (%q, %v), want (tpl-1, nil)", id, err)
		}

		// List.
		list, err := s.ListAliases(ctx, "tpl-1")
		if err != nil {
			t.Fatalf("ListAliases: %v", err)
		}
		if len(list) != 2 {
			t.Errorf("ListAliases returned %d, want 2", len(list))
		}

		// Remove.
		if err := s.RemoveAlias(ctx, "tpl-1", "latest"); err != nil {
			t.Fatalf("RemoveAlias: %v", err)
		}
		if _, err := s.ResolveAlias(ctx, "latest"); !errors.Is(err, ErrAliasNotFound) {
			t.Errorf("after RemoveAlias, ResolveAlias: got %v, want ErrAliasNotFound", err)
		}

		// Remove non-existent.
		if err := s.RemoveAlias(ctx, "tpl-1", "no-such"); !errors.Is(err, ErrAliasNotFound) {
			t.Errorf("RemoveAlias non-existent: got %v, want ErrAliasNotFound", err)
		}

		// Delete all.
		if err := s.DeleteAliases(ctx, "tpl-1"); err != nil {
			t.Fatalf("DeleteAliases: %v", err)
		}
		list, _ = s.ListAliases(ctx, "tpl-1")
		if len(list) != 0 {
			t.Errorf("after DeleteAliases, ListAliases returned %d, want 0", len(list))
		}
	})

	t.Run("Tags", func(t *testing.T) {
		s := newStore(t)
		defer s.Close()

		_ = s.CreateTemplate(ctx, &TemplateEntry{
			TemplateID: "tpl-1", Name: "t1", CreatedAt: time.Now(),
		})

		// Save.
		tag := &adapter.Tag{Name: "v1.0", TemplateID: "tpl-1", BuildID: "build-1", CreatedAt: time.Now()}
		if err := s.SaveTag(ctx, "tpl-1", tag); err != nil {
			t.Fatalf("SaveTag: %v", err)
		}
		// Replace (same name).
		replaced := &adapter.Tag{Name: "v1.0", TemplateID: "tpl-1", BuildID: "build-2", CreatedAt: time.Now()}
		if err := s.SaveTag(ctx, "tpl-1", replaced); err != nil {
			t.Fatalf("SaveTag replace: %v", err)
		}

		// List.
		list, err := s.ListTags(ctx, "tpl-1")
		if err != nil {
			t.Fatalf("ListTags: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("ListTags returned %d, want 1", len(list))
		}
		if list[0].BuildID != "build-2" {
			t.Errorf("tag BuildID = %q, want build-2 (replaced)", list[0].BuildID)
		}

		// Delete.
		if err := s.DeleteTag(ctx, "tpl-1", "v1.0"); err != nil {
			t.Fatalf("DeleteTag: %v", err)
		}
		list, _ = s.ListTags(ctx, "tpl-1")
		if len(list) != 0 {
			t.Errorf("after DeleteTag, ListTags returned %d, want 0", len(list))
		}

		// Delete non-existent.
		if err := s.DeleteTag(ctx, "tpl-1", "v1.0"); !errors.Is(err, ErrTagNotFound) {
			t.Errorf("DeleteTag non-existent: got %v, want ErrTagNotFound", err)
		}

		// Delete all.
		_ = s.SaveTag(ctx, "tpl-1", &adapter.Tag{Name: "a"})
		_ = s.SaveTag(ctx, "tpl-1", &adapter.Tag{Name: "b"})
		if err := s.DeleteTags(ctx, "tpl-1"); err != nil {
			t.Fatalf("DeleteTags: %v", err)
		}
		list, _ = s.ListTags(ctx, "tpl-1")
		if len(list) != 0 {
			t.Errorf("after DeleteTags, ListTags returned %d, want 0", len(list))
		}
	})
}

func TestMemoryTemplateStore_Contract(t *testing.T) {
	templateStoreContract(t, func(t *testing.T) TemplateStore {
		return NewMemoryTemplateStore()
	})
}
