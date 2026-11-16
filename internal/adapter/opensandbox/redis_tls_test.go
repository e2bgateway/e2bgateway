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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeRedisAddr(t *testing.T) {
	tests := []struct {
		in       string
		wantAddr string
		wantTLS  bool
	}{
		{"redis.example.com:6379", "redis.example.com:6379", false},
		{"rediss://redis.example.com:6379", "redis.example.com:6379", true},
		{"localhost:6379", "localhost:6379", false},
	}
	for _, tt := range tests {
		addr, schemeTLS := normalizeRedisAddr(tt.in)
		assert.Equal(t, tt.wantAddr, addr)
		assert.Equal(t, tt.wantTLS, schemeTLS)
	}
}

func TestRedisTLSConfig(t *testing.T) {
	tests := []struct {
		name        string
		addr        string
		tlsEnabled  bool
		hasPassword bool
		wantTLS     bool
		wantErr     bool
	}{
		{"no password no tls stays cleartext", "redis:6379", false, false, false, false},
		{"tls enabled returns config", "redis.example.com:6379", true, true, true, false},
		{"tls without password allowed", "redis.example.com:6379", true, false, true, false},
		{"password to remote without tls is rejected", "redis.example.com:6379", false, true, false, true},
		{"password to loopback without tls is allowed", "127.0.0.1:6379", false, true, false, false},
		{"password to localhost without tls is allowed", "localhost:6379", false, true, false, false},
		{"password to ipv6 loopback without tls is allowed", "[::1]:6379", false, true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := redisTLSConfig(tt.addr, tt.tlsEnabled, tt.hasPassword)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.wantTLS {
				require.NotNil(t, cfg)
				assert.Equal(t, "redis.example.com", cfg.ServerName)
			} else {
				assert.Nil(t, cfg)
			}
		})
	}
}
