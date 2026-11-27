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
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/e2bgateway/e2bgateway/internal/config"
)

func tieredTestConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Cache.Tiered.Enabled = true
	cfg.Cache.Tiered.L1 = config.TieredL1Config{MaxSize: 100, DefaultTTL: 30 * time.Second}
	cfg.Cache.Tiered.L2 = config.TieredL2Config{Timeout: 100 * time.Millisecond, KeyPrefix: "e2bgw:cache:", PubsubChannel: "e2bgw:cache:invalidate"}
	return cfg
}

func TestInitCaches_Disabled(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	require.NoError(t, s.initCaches())
	assert.Nil(t, s.sandboxCache)
	assert.Nil(t, s.templateCache)
	assert.Nil(t, s.healthCache)
	assert.Nil(t, s.invalidator)
}

func TestInitCaches_L1Only(t *testing.T) {
	s := &Server{cfg: tieredTestConfig()}
	require.NoError(t, s.initCaches())
	assert.NotNil(t, s.sandboxCache)
	assert.NotNil(t, s.templateCache)
	assert.NotNil(t, s.healthCache)
	assert.Nil(t, s.invalidator) // no l2.address → no Pub/Sub
}

func TestInitCaches_WithRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	cfg := tieredTestConfig()
	cfg.Cache.Tiered.L2.Address = mr.Addr() // loopback → plaintext OK
	s := &Server{cfg: cfg}
	require.NoError(t, s.initCaches())
	require.NotNil(t, s.invalidator)
	require.NotNil(t, s.cacheCancel)
	s.cacheCancel()
	<-s.cacheDone // Run must exit promptly on cancel
}

func TestInitCaches_UnreachableRedisFailsStartup(t *testing.T) {
	mr := miniredis.RunT(t)
	addr := mr.Addr()
	mr.Close()
	cfg := tieredTestConfig()
	cfg.Cache.Tiered.L2.Address = addr
	s := &Server{cfg: cfg}
	err := s.initCaches()
	assert.ErrorContains(t, err, "unreachable")
}
