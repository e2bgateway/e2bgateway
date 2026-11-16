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
	"crypto/tls"
	"fmt"
	"net"
	"strings"
)

// redissPrefix is the addr scheme that implicitly enables TLS, mirroring the
// URL convention go-redis uses in ParseURL.
const redissPrefix = "rediss://"

// normalizeRedisAddr strips the rediss:// scheme from addr, reporting whether
// the scheme requested TLS.
func normalizeRedisAddr(addr string) (string, bool) {
	if rest, ok := strings.CutPrefix(addr, redissPrefix); ok {
		return rest, true
	}
	return addr, false
}

// isLoopbackHost reports whether host (bracket-stripped, port-less) refers to
// the local machine. Loopback connections are exempt from the TLS requirement
// so local development and miniredis-based tests keep working in cleartext.
func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// redisTLSConfig builds the TLS configuration for a Redis connection, shared
// by the templateStore and the warm pool stateStore.
//
// Enforcement (CWE-319, PR #82 review): a password-bearing connection to a
// non-loopback address MUST use TLS — enabled via "tls: true" or a rediss://
// addr. Anything else fails adapter construction instead of silently sending
// the credential in cleartext. Verification uses the system CA pool and the
// addr host as ServerName; there is deliberately no insecure-skip-verify
// escape hatch.
func redisTLSConfig(addr string, tlsEnabled, hasPassword bool) (*tls.Config, error) {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	if !tlsEnabled {
		if hasPassword && !isLoopbackHost(host) {
			return nil, fmt.Errorf(
				"redis addr %q is not loopback and a password is configured: "+
					"enable TLS (tls: true, or a rediss:// addr) to avoid sending "+
					"credentials in cleartext", addr)
		}
		return nil, nil
	}
	return &tls.Config{
		ServerName: strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"),
		MinVersion: tls.VersionTLS12,
	}, nil
}
