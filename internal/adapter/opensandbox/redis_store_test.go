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
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedisTemplateStore_Contract(t *testing.T) {
	templateStoreContract(t, func(t *testing.T) TemplateStore {
		mr, err := miniredis.Run()
		if err != nil {
			t.Fatalf("miniredis: %v", err)
		}
		t.Cleanup(mr.Close)

		client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = client.Close() })

		return NewRedisTemplateStoreWithClient(client, "test:")
	})
}

func TestRedisTemplateStore_PingRequired(t *testing.T) {
	// NewRedisTemplateStore must fail if the server is unreachable.
	_, err := NewRedisTemplateStore(context.Background(), RedisConfig{
		Addr: "localhost:1", // unreachable
	})
	if err == nil {
		t.Error("expected error for unreachable server")
	}
}

func TestRedisTemplateStore_EmptyAddr(t *testing.T) {
	_, err := NewRedisTemplateStore(context.Background(), RedisConfig{})
	if err == nil {
		t.Error("expected error for empty addr")
	}
}

// A second store commits a disjoint change after the first writer reads.
// The first writer must retry its callback against the committed state.
func TestRedisTemplateStore_UpdateTemplate_ConcurrentFields(t *testing.T) {
	mr := miniredis.RunT(t)
	newStore := func() *RedisTemplateStore {
		client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = client.Close() })
		return NewRedisTemplateStoreWithClient(client, "test:")
	}
	first, second := newStore(), newStore()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, first.CreateTemplate(ctx, &TemplateEntry{TemplateID: "tpl", Name: "original"}))
	read := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		calls := 0
		done <- first.UpdateTemplate(ctx, "tpl", func(entry *TemplateEntry) error {
			calls++
			if calls == 1 {
				close(read)
				select {
				case <-resume:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			entry.Name = "renamed"
			return nil
		})
	}()
	select {
	case <-read:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	err := second.UpdateTemplate(ctx, "tpl", func(entry *TemplateEntry) error {
		entry.CPUCount = 8
		return nil
	})
	close(resume)
	require.NoError(t, err)
	require.NoError(t, <-done)
	got, err := first.GetTemplate(ctx, "tpl")
	require.NoError(t, err)
	assert.Equal(t, "renamed", got.Name)
	assert.Equal(t, 8, got.CPUCount)
}
