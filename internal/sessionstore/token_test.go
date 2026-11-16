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
	for i := 0; i < n; i++ {
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

func TestOverwriteToken(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	exp := time.Now().Add(time.Hour)
	if err := OverwriteToken(ctx, s, "sb2", "tok-old", exp, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := OverwriteToken(ctx, s, "sb2", "tok-new", exp, time.Hour); err != nil {
		t.Fatal(err)
	}
	got, _, _, err := GetToken(ctx, s, "sb2")
	if err != nil || got != "tok-new" {
		t.Fatalf("after overwrite: (%q,%v)", got, err)
	}
}
