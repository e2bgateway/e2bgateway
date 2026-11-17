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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenRoundTripAndExpiry(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	exp := time.Now().Add(time.Hour).Truncate(time.Second)

	if _, _, ok, err := GetToken(ctx, s, "sb1"); err != nil || ok {
		t.Fatalf("empty store: (%v,%v)", ok, err)
	}
	won, err := StoreTokenNX(ctx, s, "sb1", "tok-1", exp, time.Hour)
	if err != nil || !won {
		t.Fatalf("StoreTokenNX: (%v,%v)", won, err)
	}

	got, gotExp, ok, err := GetToken(ctx, s, "sb1")
	if err != nil || !ok || got != "tok-1" {
		t.Fatalf("GetToken: (%q,%v,%v)", got, ok, err)
	}
	if !gotExp.Equal(exp) {
		t.Errorf("expiresAt: got %v, want %v (stored expiry must survive round-trip)", gotExp, exp)
	}

	valid, err := ValidateToken(ctx, s, "sb1", "tok-1")
	if err != nil || !valid {
		t.Fatalf("ValidateToken(correct): (%v,%v)", valid, err)
	}
	valid, err = ValidateToken(ctx, s, "sb1", "wrong")
	if err != nil || valid {
		t.Fatalf("ValidateToken(wrong): (%v,%v)", valid, err)
	}
	valid, err = ValidateToken(ctx, s, "unknown-sandbox", "tok-1")
	if err != nil || valid {
		t.Fatalf("ValidateToken(missing sandbox): (%v,%v)", valid, err)
	}
}

// Two "replicas" racing GetOrCreate-style minting must converge on ONE token.
func TestTokenNXArbitrationConcurrent(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	const n = 16
	var wg sync.WaitGroup
	results := make([]string, n)
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			tok, _, ok, err := GetToken(ctx, s, "sb-race")
			if err != nil {
				t.Errorf("replica %d GetToken: %v", i, err)
				return
			}
			if !ok {
				candidate := fmtToken(i)
				won, err := StoreTokenNX(ctx, s, "sb-race", candidate, time.Now().Add(time.Hour), time.Hour)
				if err != nil {
					t.Errorf("replica %d StoreTokenNX: %v", i, err)
					return
				}
				if !won {
					tok, _, _, err = GetToken(ctx, s, "sb-race")
					if err != nil {
						t.Errorf("replica %d re-GetToken: %v", i, err)
						return
					}
				} else {
					tok = candidate
				}
			}
			results[i] = tok
		}(i)
	}
	wg.Wait()
	for i, r := range results {
		if r != results[0] {
			t.Fatalf("replica %d got token %q, replica 0 got %q — arbitration broken", i, r, results[0])
		}
	}
}

func fmtToken(i int) string { return "tok-" + string(rune('a'+i)) }

func TestMintTokenReturnsOwnOnWin(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	tok, exp, err := MintToken(ctx, s, "sb-mint", func() (string, time.Time, error) {
		return "minted-1", time.Now().Add(time.Hour), nil
	})
	require.NoError(t, err)
	assert.Equal(t, "minted-1", tok)
	assert.False(t, exp.IsZero())

	// A second mint must return the FIRST winner, not mint again.
	tok2, _, err := MintToken(ctx, s, "sb-mint", func() (string, time.Time, error) {
		t.Error("mint must not be called when a live entry exists via SetNX loss")
		return "minted-2", time.Now().Add(time.Hour), nil
	})
	require.NoError(t, err)
	assert.Equal(t, "minted-1", tok2)
}

// A vanishing winner (TTL edge) must trigger a conditional retry — never a
// blind overwrite of a concurrent new winner.
func TestMintTokenRetriesWhenWinnerVanishes(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()

	// Pre-seed a winner that expires immediately: the first SetNX loses, the
	// follow-up Get misses, and the retry loop must win on round 2.
	won, err := s.SetNX(ctx, TokenKey("sb-vanish"), `{"token":"ghost","expiresAt":"2000-01-01T00:00:00Z"}`, time.Millisecond)
	require.NoError(t, err)
	require.True(t, won)
	time.Sleep(5 * time.Millisecond)

	tok, _, err := MintToken(ctx, s, "sb-vanish", func() (string, time.Time, error) {
		return "retry-winner", time.Now().Add(time.Hour), nil
	})
	require.NoError(t, err)
	assert.Equal(t, "retry-winner", tok)
}

func TestMintTokenPropagatesMintError(t *testing.T) {
	s := NewMemory()
	_, _, err := MintToken(context.Background(), s, "sb-err", func() (string, time.Time, error) {
		return "", time.Time{}, assert.AnError
	})
	require.ErrorIs(t, err, assert.AnError)
}

// CWE-613: a mint whose expiresAt has already elapsed must be rejected
// BEFORE the store write — a non-positive TTL would be stored as
// "never expires" by the memory store.
func TestMintTokenRejectsElapsedExpiry(t *testing.T) {
	s := NewMemory()
	_, _, err := MintToken(context.Background(), s, "sb-elapsed", func() (string, time.Time, error) {
		return "tok-stale", time.Now().Add(-time.Second), nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expired before it could be stored")

	// Nothing must have been written.
	_, _, ok, err := GetToken(context.Background(), s, "sb-elapsed")
	require.NoError(t, err)
	assert.False(t, ok)
}

// Defense in depth: even if a store entry outlives its stated expiresAt
// (write delays extend the store TTL past ExpiresAt), GetToken must treat
// it as expired.
func TestGetTokenRejectsEntryPastExpiresAt(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)
	// Store with a LONG store-TTL but an expiresAt already in the past.
	_, err := s.SetNX(ctx, TokenKey("sb-past"), `{"token":"tok-ghost","expiresAt":"`+past.Format(time.RFC3339Nano)+`"}`, time.Hour)
	require.NoError(t, err)
	require.True(t, err == nil)

	tok, _, ok, err := GetToken(ctx, s, "sb-past")
	require.NoError(t, err)
	assert.False(t, ok, "entry past its expiresAt must read as a miss")
	assert.Empty(t, tok)

	valid, err := ValidateToken(ctx, s, "sb-past", "tok-ghost")
	require.NoError(t, err)
	assert.False(t, valid)
}

// The physical store TTL can outlive the token's stated expiresAt (delayed
// write). SetNX then loses forever while GetToken misses — MintToken must
// replace the stale entry instead of failing to converge (CWE-613 follow-up).
func TestMintTokenReplacesLogicallyExpiredEntry(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)
	raw := `{"token":"tok-ghost","expiresAt":"` + past.Format(time.RFC3339Nano) + `"}`
	// Physical TTL of an hour, logical expiry already past.
	require.NoError(t, s.Set(ctx, TokenKey("sb-stale"), raw, time.Hour))

	tok, exp, err := MintToken(ctx, s, "sb-stale", func() (string, time.Time, error) {
		return "tok-fresh", time.Now().Add(time.Hour), nil
	})
	require.NoError(t, err, "MintToken must converge despite the stale physical entry")
	assert.Equal(t, "tok-fresh", tok)
	assert.True(t, exp.After(time.Now()))

	// The fresh token is what's live now.
	valid, err := ValidateToken(ctx, s, "sb-stale", "tok-fresh")
	require.NoError(t, err)
	assert.True(t, valid)
}
