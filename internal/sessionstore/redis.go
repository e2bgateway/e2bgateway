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
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/redisx"
)

// redisStore is the cluster-shared Store implementation backed by go-redis.
type redisStore struct {
	client redis.UniversalClient
	prefix string
}

// New builds the configured Store. For redis it verifies connectivity with a
// Ping so misconfiguration fails startup instead of every request.
func New(cfg config.SessionStoreConfig) (Store, error) {
	switch cfg.Type {
	case "", "memory":
		return NewMemory(), nil
	case "redis":
		addr, schemeTLS := redisx.NormalizeRedisAddr(cfg.Addr)
		tlsCfg, err := redisx.TLSConfig(addr, cfg.EnableTLS || schemeTLS, cfg.Password != "")
		if err != nil {
			return nil, fmt.Errorf("session store redis TLS config: %w", err)
		}
		// Stricter than the warm-pool/template-store rule (CWE-319): the
		// session store carries bearer tokens, not just credentials, so ANY
		// non-loopback connection must use TLS regardless of password —
		// unless the operator explicitly opted into a trusted-network
		// plaintext deployment via allowPlaintext.
		if tlsCfg == nil && redisx.AddrRequiresTLS(addr) {
			if !cfg.AllowPlaintext {
				return nil, fmt.Errorf("session store: redis addr %q is not loopback: enable TLS (tls: true, or a rediss:// addr) — the store carries envd bearer tokens that must not traverse the network in cleartext; on a trusted network set allowPlaintext: true to opt out explicitly", cfg.Addr)
			}
			slog.Warn("session store: sending envd bearer tokens to a non-loopback redis WITHOUT TLS (allowPlaintext is set); ensure the network is trusted (e.g. in-cluster with NetworkPolicy)",
				"op", "session_store_init", "addr", cfg.Addr)
		}
		client := redis.NewClient(&redis.Options{
			Addr:      addr,
			Password:  cfg.Password,
			DB:        cfg.DB,
			TLSConfig: tlsCfg,
		})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Ping(ctx).Err(); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("session store redis unreachable at %s: %w", cfg.Addr, err)
		}
		return &redisStore{client: client, prefix: cfg.KeyPrefix}, nil
	default:
		return nil, fmt.Errorf("session store: unsupported type %q", cfg.Type)
	}
}

// NewRedisWithClient builds a redisStore around an existing client (test seam).
func NewRedisWithClient(client redis.UniversalClient, keyPrefix string) Store {
	return &redisStore{client: client, prefix: keyPrefix}
}

func (r *redisStore) key(k string) string { return r.prefix + k }

func (r *redisStore) Get(ctx context.Context, key string) (string, bool, error) {
	v, err := r.client.Get(ctx, r.key(key)).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("session store get: %w", err)
	}
	return v, true, nil
}

func (r *redisStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	// go-redis: expiration 0 means "no expiry".
	if err := r.client.Set(ctx, r.key(key), value, persistZero(ttl)).Err(); err != nil {
		return fmt.Errorf("session store set: %w", err)
	}
	return nil
}

func (r *redisStore) SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	ok, err := r.client.SetNX(ctx, r.key(key), value, persistZero(ttl)).Result()
	if err != nil {
		return false, fmt.Errorf("session store setnx: %w", err)
	}
	return ok, nil
}

func (r *redisStore) Touch(ctx context.Context, key string, ttl time.Duration) error {
	if ttl <= 0 {
		return nil
	}
	// PEXPIRE (not EXPIRE) to preserve sub-second TTL precision — EXPIRE
	// truncates to whole seconds. It only affects an existing key; a missing
	// key is a no-op (false result, no error) — Touch must never resurrect a
	// deleted entry.
	if err := r.client.PExpire(ctx, r.key(key), ttl).Err(); err != nil {
		return fmt.Errorf("session store touch: %w", err)
	}
	return nil
}

func (r *redisStore) Delete(ctx context.Context, key string) error {
	if err := r.client.Del(ctx, r.key(key)).Err(); err != nil {
		return fmt.Errorf("session store delete: %w", err)
	}
	return nil
}

// escapeRedisGlob escapes MATCH metacharacters so a configured key prefix
// (e.g. "tenant[1]:") matches literally instead of as a glob.
func escapeRedisGlob(s string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		`*`, `\*`,
		`?`, `\?`,
		`[`, `\[`,
		`]`, `\]`,
	).Replace(s)
}

func (r *redisStore) Scan(ctx context.Context, prefix string, visit func(key, value string)) error {
	pattern := escapeRedisGlob(r.key(prefix)) + "*"
	iter := r.client.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		physKey := iter.Val()
		v, err := r.client.Get(ctx, physKey).Result()
		if errors.Is(err, redis.Nil) {
			continue // expired between SCAN and GET
		}
		if err != nil {
			return fmt.Errorf("session store scan: %w", err)
		}
		visit(strings.TrimPrefix(physKey, r.prefix), v)
	}
	return iter.Err()
}

// persistZero maps ttl<=0 to go-redis' 0 ("persist").
func persistZero(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return 0
	}
	return ttl
}
