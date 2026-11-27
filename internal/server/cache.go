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

package server

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/api/dto"
	"github.com/e2bgateway/e2bgateway/internal/cache"
	"github.com/e2bgateway/e2bgateway/internal/redisx"
)

// initCaches builds the tiered caches when cache.tiered.enabled. Disabled
// (default) leaves all cache fields nil → handlers pass through unchanged.
// Enabled without l2.address runs L1-only. An unreachable configured Redis
// fails startup (misconfiguration should surface immediately, matching the
// sessionstore precedent); runtime failures degrade to L1 per spec §3.3.
func (s *Server) initCaches() error {
	tc := s.cfg.Cache.Tiered
	if !tc.Enabled {
		return nil
	}

	s.healthCache = cache.New(100, cache.BackendHealthTTL)

	opts := cache.TieredOptions{
		L1MaxSize: tc.L1.MaxSize,
		L1TTL:     tc.L1.DefaultTTL,
		Timeout:   tc.L2.Timeout,
	}

	if tc.L2.Address != "" {
		addr, schemeTLS := redisx.NormalizeRedisAddr(tc.L2.Address)
		tlsCfg, err := redisx.TLSConfig(addr, tc.L2.TLS || schemeTLS, tc.L2.Password != "")
		if err != nil {
			return fmt.Errorf("tiered cache redis TLS config: %w", err)
		}
		// Cached payloads carry no credentials (verified: SandboxInfo and
		// Template have no token fields), so non-loopback plaintext is a
		// loud WARN rather than sessionstore's fail-closed. Password over
		// plaintext is already rejected by config validation.
		if tlsCfg == nil && redisx.AddrRequiresTLS(addr) {
			slog.Warn("tiered cache: non-loopback redis WITHOUT TLS; cached payloads carry no credentials but TLS is recommended",
				"op", "tiered_cache_init", "addr", tc.L2.Address)
		}
		client := redis.NewClient(&redis.Options{
			Addr:      addr,
			Password:  tc.L2.Password,
			DB:        tc.L2.DB,
			TLSConfig: tlsCfg,
		})
		pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Ping(pingCtx).Err(); err != nil {
			_ = client.Close()
			return fmt.Errorf("tiered cache redis unreachable at %s: %w", tc.L2.Address, err)
		}
		opts.L2 = cache.NewRedisCache(cache.WrapRedisClient(client), tc.L2.KeyPrefix, tc.L1.DefaultTTL)
		inv := cache.NewInvalidator(client, tc.L2.PubsubChannel)
		opts.Invalidator = inv
		s.invalidator = inv
	}

	sbOpts := opts
	sbOpts.Name = "sandbox_metadata"
	s.sandboxCache = cache.NewTiered[dto.SandboxInfo](sbOpts)

	tmplOpts := opts
	tmplOpts.Name = "template_list"
	s.templateCache = cache.NewTiered[[]*adapter.Template](tmplOpts)

	if s.invalidator != nil {
		s.invalidator.Register("sandbox_metadata", s.sandboxCache.OnRemoteInvalidate)
		s.invalidator.Register("template_list", s.templateCache.OnRemoteInvalidate)
		ctx, cancel := context.WithCancel(context.Background())
		s.cacheCancel = cancel
		s.cacheDone = make(chan struct{})
		go func() {
			defer close(s.cacheDone)
			s.invalidator.Run(ctx)
		}()
	}

	slog.Info("tiered cache enabled", "l2", tc.L2.Address != "", "l1MaxSize", tc.L1.MaxSize)
	return nil
}
