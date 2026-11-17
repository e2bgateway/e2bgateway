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
	// Defense in depth (CWE-613): never hand out a token past its stated
	// expiry, even if the store entry outlives it (write delays can extend
	// the store TTL slightly past ExpiresAt).
	if !e.ExpiresAt.IsZero() && time.Now().After(e.ExpiresAt) {
		return "", time.Time{}, false, nil
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

// mintAttempts bounds the SetNX/Get arbitration loop. Each lost round means
// a winner's entry appeared and vanished between two calls (TTL edge) —
// retrying keeps every write conditional so a concurrent winner is never
// blindly overwritten.
const mintAttempts = 3

// MintToken stores a freshly minted token under SetNX arbitration and
// returns the token that is now live for the sandbox — the caller's own if
// it won, the winner's if it lost. mint is invoked per attempt (a signed-URL
// mint may need a fresh expiry). If the winning entry vanishes between a
// lost SetNX and the follow-up Get, the sequence retries; after mintAttempts
// rounds without convergence it errors instead of overwriting blindly.
func MintToken(ctx context.Context, s Store, sandboxID string, mint func() (token string, expiresAt time.Time, err error)) (string, time.Time, error) {
	for range mintAttempts {
		// Cheap read first: avoid an expensive mint (e.g. the signed-endpoint
		// RPC) when a live token already exists.
		if tok, exp, ok, err := GetToken(ctx, s, sandboxID); err != nil {
			return "", time.Time{}, err
		} else if ok {
			return tok, exp, nil
		}
		token, expiresAt, err := mint()
		if err != nil {
			return "", time.Time{}, err
		}
		// Guard the TTL before writing (CWE-613): a non-positive TTL would
		// be stored as "never expires", outliving the token's own expiry.
		ttl := time.Until(expiresAt)
		if ttl <= 0 {
			return "", time.Time{}, fmt.Errorf("minted token for sandbox expired before it could be stored")
		}
		won, err := StoreTokenNX(ctx, s, sandboxID, token, expiresAt, ttl)
		if err != nil {
			return "", time.Time{}, err
		}
		if won {
			return token, expiresAt, nil
		}
		// Lost the race — return the winner's token.
		if tok, exp, ok, err := GetToken(ctx, s, sandboxID); err != nil {
			return "", time.Time{}, err
		} else if ok {
			return tok, exp, nil
		}
		// Winner's entry vanished (TTL edge): loop retries SetNX.
	}
	return "", time.Time{}, fmt.Errorf("token arbitration for sandbox did not converge after %d attempts", mintAttempts)
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
