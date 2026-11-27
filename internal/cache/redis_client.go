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
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

type universalClientAdapter struct{ client redis.UniversalClient }

// WrapRedisClient adapts a go-redis client to the RedisCacheClient interface.
// redis.Nil (key missing) is normalized to ("", nil) — a cache miss.
func WrapRedisClient(client redis.UniversalClient) RedisCacheClient {
	return &universalClientAdapter{client: client}
}

func (a *universalClientAdapter) Set(ctx context.Context, key string, value any, expiration time.Duration) error {
	return a.client.Set(ctx, key, value, expiration).Err()
}

func (a *universalClientAdapter) Get(ctx context.Context, key string) (string, error) {
	val, err := a.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return val, err
}

func (a *universalClientAdapter) Del(ctx context.Context, keys ...string) error {
	return a.client.Del(ctx, keys...).Err()
}

func (a *universalClientAdapter) Exists(ctx context.Context, keys ...string) (int64, error) {
	return a.client.Exists(ctx, keys...).Result()
}
