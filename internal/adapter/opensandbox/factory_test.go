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
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// TestFactory_TemplateStore_Memory verifies the factory creates a
// MemoryTemplateStore when templateStore.type is "memory" (or unspecified).
func TestFactory_TemplateStore_Memory(t *testing.T) {
	store, err := buildTemplateStore(map[string]any{
		"type": "memory",
	})
	if err != nil {
		t.Fatalf("buildTemplateStore(memory): %v", err)
	}
	if _, ok := store.(*MemoryTemplateStore); !ok {
		t.Errorf("expected *MemoryTemplateStore, got %T", store)
	}
	_ = store.Close()
}

// TestFactory_TemplateStore_DefaultIsMemory verifies that an empty config
// defaults to memory.
func TestFactory_TemplateStore_DefaultIsMemory(t *testing.T) {
	store, err := buildTemplateStore(map[string]any{})
	if err != nil {
		t.Fatalf("buildTemplateStore(empty): %v", err)
	}
	if _, ok := store.(*MemoryTemplateStore); !ok {
		t.Errorf("expected *MemoryTemplateStore for default, got %T", store)
	}
	_ = store.Close()
}

// TestFactory_TemplateStore_Redis verifies the factory creates a
// RedisTemplateStore when templateStore.type is "redis".
func TestFactory_TemplateStore_Redis(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	store, err := buildTemplateStore(map[string]any{
		"type": "redis",
		"addr": mr.Addr(),
	})
	if err != nil {
		t.Fatalf("buildTemplateStore(redis): %v", err)
	}
	if _, ok := store.(*RedisTemplateStore); !ok {
		t.Errorf("expected *RedisTemplateStore, got %T", store)
	}
	_ = store.Close()
}

// TestFactory_TemplateStore_Unknown verifies the factory rejects unknown types.
func TestFactory_TemplateStore_Unknown(t *testing.T) {
	_, err := buildTemplateStore(map[string]any{
		"type": "unknown-backend",
	})
	if err == nil {
		t.Error("expected error for unknown type")
	}
}

// TestFactory_TemplateStore_RedisMissingAddr verifies the factory rejects
// redis config without addr.
func TestFactory_TemplateStore_RedisMissingAddr(t *testing.T) {
	_, err := buildTemplateStore(map[string]any{
		"type": "redis",
	})
	if err == nil {
		t.Error("expected error for redis without addr")
	}
}

// TestRedisTemplateStore_PersistsAcrossClients verifies that data written by
// one RedisTemplateStore instance is visible to another (shared state).
// This is the key property that differentiates Redis from memory store.
func TestRedisTemplateStore_PersistsAcrossClients(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	ctx := context.Background()

	// First client writes.
	client1 := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store1 := NewRedisTemplateStoreWithClient(client1, "persist:")
	defer store1.Close()

	entry := &TemplateEntry{
		TemplateID: "persist-tpl",
		Name:       "persistent-template",
		ImageURI:   "alpine:3.20",
	}
	if err := store1.CreateTemplate(ctx, entry); err != nil {
		t.Fatalf("store1.CreateTemplate: %v", err)
	}

	// Second client (different instance, same Redis) reads.
	client2 := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store2 := NewRedisTemplateStoreWithClient(client2, "persist:")
	defer store2.Close()

	got, err := store2.GetTemplate(ctx, "persist-tpl")
	if err != nil {
		t.Fatalf("store2.GetTemplate: %v", err)
	}
	if got.Name != "persistent-template" {
		t.Errorf("store2 got Name=%q, want 'persistent-template'", got.Name)
	}
	if got.ImageURI != "alpine:3.20" {
		t.Errorf("store2 got ImageURI=%q, want 'alpine:3.20'", got.ImageURI)
	}
}
