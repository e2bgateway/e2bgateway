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
	"encoding/json"
	"fmt"
	"time"
)

// tokenEntry is the JSON value stored under TokenKey. expiresAt is stored
// explicitly so callers report the token's REAL expiry instead of
// re-stamping now+TTL on every lookup.
type tokenEntry struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// TokenKey is the logical store key for a sandbox's envd access token.
func TokenKey(sandboxID string) string { return "token:" + sandboxID }

// GetToken returns the stored token for sandboxID, if any.
func GetToken(ctx context.Context, s Store, sandboxID string) (string, time.Time, bool, error) {
	raw, ok, err := s.Get(ctx, TokenKey(sandboxID))
	if err != nil || !ok {
		return "", time.Time{}, false, err
	}
	var e tokenEntry
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return "", time.Time{}, false, fmt.Errorf("decoding stored token entry: %w", err)
	}
	return e.Token, e.ExpiresAt, true, nil
}

// StoreTokenNX writes the token only if none is live; won=false means
// another replica already minted one — callers must GetToken and return
// the winner's value so one sandbox has exactly one live token.
func StoreTokenNX(ctx context.Context, s Store, sandboxID, token string, expiresAt time.Time, ttl time.Duration) (bool, error) {
	raw, err := json.Marshal(tokenEntry{Token: token, ExpiresAt: expiresAt})
	if err != nil {
		return false, fmt.Errorf("encoding token entry: %w", err)
	}
	return s.SetNX(ctx, TokenKey(sandboxID), string(raw), ttl)
}

// OverwriteToken unconditionally replaces the token entry. Fallback for the
// rare race where the winner's entry expires between a lost SetNX and the
// follow-up Get.
func OverwriteToken(ctx context.Context, s Store, sandboxID, token string, expiresAt time.Time, ttl time.Duration) error {
	raw, err := json.Marshal(tokenEntry{Token: token, ExpiresAt: expiresAt})
	if err != nil {
		return fmt.Errorf("encoding token entry: %w", err)
	}
	return s.Set(ctx, TokenKey(sandboxID), string(raw), ttl)
}

// ValidateToken reports whether token matches the live stored entry.
// A missing entry is (false, nil); store failures propagate as errors so
// callers can answer 502 rather than 401 during outages.
func ValidateToken(ctx context.Context, s Store, sandboxID, token string) (bool, error) {
	stored, _, ok, err := GetToken(ctx, s, sandboxID)
	if err != nil || !ok {
		return false, err
	}
	return stored == token, nil
}
