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
