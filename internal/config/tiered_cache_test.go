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

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const tieredTestBase = `
server:
  http:
    address: ":8080"
backends:
  - name: mock
    type: mock
    enabled: true
`

func writeTieredConfig(t *testing.T, extra string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cfg.yaml")
	require.NoError(t, os.WriteFile(path, []byte(tieredTestBase+extra), 0o600))
	return path
}

func TestTieredCacheDefaults(t *testing.T) {
	cfg, err := Load(writeTieredConfig(t, ""))
	require.NoError(t, err)
	assert.False(t, cfg.Cache.Tiered.Enabled)
	assert.Equal(t, 10000, cfg.Cache.Tiered.L1.MaxSize)
	assert.Equal(t, 30*time.Second, cfg.Cache.Tiered.L1.DefaultTTL)
	assert.Equal(t, "e2bgw:cache:", cfg.Cache.Tiered.L2.KeyPrefix)
	assert.Equal(t, "e2bgw:cache:invalidate", cfg.Cache.Tiered.L2.PubsubChannel)
	assert.Equal(t, 200*time.Millisecond, cfg.Cache.Tiered.L2.Timeout)
}

func TestTieredCacheParseCamelAndLowercase(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{"camelCase", `
cache:
  tiered:
    enabled: true
    l1:
      maxSize: 500
      defaultTTL: 45s
    l2:
      address: "127.0.0.1:6379"
      pubsubChannel: "test:invalidate"
      timeout: 150ms
`},
		{"lowercase", `
cache:
  tiered:
    enabled: true
    l1:
      maxsize: 500
      defaultttl: 45s
    l2:
      address: "127.0.0.1:6379"
      pubsubchannel: "test:invalidate"
      timeout: 150ms
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeTieredConfig(t, tc.yaml))
			require.NoError(t, err)
			assert.True(t, cfg.Cache.Tiered.Enabled)
			assert.Equal(t, 500, cfg.Cache.Tiered.L1.MaxSize)
			assert.Equal(t, 45*time.Second, cfg.Cache.Tiered.L1.DefaultTTL)
			assert.Equal(t, "127.0.0.1:6379", cfg.Cache.Tiered.L2.Address)
			assert.Equal(t, "test:invalidate", cfg.Cache.Tiered.L2.PubsubChannel)
			assert.Equal(t, 150*time.Millisecond, cfg.Cache.Tiered.L2.Timeout)
		})
	}
}

func TestTieredCacheValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*TieredCacheConfig)
		wantErr bool
	}{
		{"disabled skips validation", func(c *TieredCacheConfig) { c.Enabled = false; c.L1.MaxSize = 0 }, false},
		{"valid L1 only", func(c *TieredCacheConfig) {}, false},
		{"bad maxSize", func(c *TieredCacheConfig) { c.L1.MaxSize = 0 }, true},
		{"bad timeout", func(c *TieredCacheConfig) { c.L2.Timeout = 0 }, true},
		{"password plaintext non-loopback", func(c *TieredCacheConfig) {
			c.L2.Address = "redis.prod:6379"
			c.L2.Password = "secret"
		}, true},
		{"password with TLS ok", func(c *TieredCacheConfig) {
			c.L2.Address = "redis.prod:6379"
			c.L2.Password = "secret"
			c.L2.TLS = true
		}, false},
		{"password plaintext loopback ok", func(c *TieredCacheConfig) {
			c.L2.Address = "127.0.0.1:6379"
			c.L2.Password = "secret"
		}, false},
		{"negative db", func(c *TieredCacheConfig) {
			c.L2.Address = "127.0.0.1:6379"
			c.L2.DB = -1
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := TieredCacheConfig{
				Enabled: true,
				L1:      TieredL1Config{MaxSize: 10, DefaultTTL: time.Second},
				L2:      TieredL2Config{Timeout: 100 * time.Millisecond},
			}
			tc.mutate(&c)
			err := c.validate()
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
