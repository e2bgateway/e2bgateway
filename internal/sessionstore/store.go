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
	"time"
)

// Store is a TTL-aware string key-value store shared across gateway
// replicas. The memory implementation keeps per-process state (single
// replica); the redis implementation shares state cluster-wide. Keys are
// logical ("token:{id}", "routing:{id}"); implementations prepend any
// configured key prefix. Error returns are always real failures — a missing
// or expired key is (zero, false, nil), never an error.
type Store interface {
	Get(ctx context.Context, key string) (value string, ok bool, err error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error)
	Delete(ctx context.Context, key string) error
	Scan(ctx context.Context, prefix string, visit func(key, value string)) error
}
