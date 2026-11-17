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

package agentsandbox

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/e2bgateway/e2bgateway/internal/sessionstore"
)

func newTokenTestAdapter(store sessionstore.Store) *Adapter {
	a := &Adapter{name: "agentsandbox-test"}
	a.SetSessionStore(store)
	return a
}

func TestGetAccessTokenSharedAcrossInstances(t *testing.T) {
	store := sessionstore.NewMemory()
	a1 := newTokenTestAdapter(store)
	a2 := newTokenTestAdapter(store) // "second replica"
	ctx := context.Background()

	tok1, err := a1.GetAccessToken(ctx, "sb-shared")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok1.Token, "envd_sb-shared_") {
		t.Errorf("unexpected token format: %q", tok1.Token)
	}

	tok2, err := a2.GetAccessToken(ctx, "sb-shared")
	if err != nil {
		t.Fatal(err)
	}
	if tok2.Token != tok1.Token {
		t.Fatalf("replica 2 minted a different token: %q vs %q", tok2.Token, tok1.Token)
	}

	// ExpiresAt must be the stored expiry, not re-stamped now+1h.
	if !tok2.ExpiresAt.Equal(tok1.ExpiresAt) {
		t.Errorf("ExpiresAt must be the stored expiry verbatim: got %v, want %v", tok2.ExpiresAt, tok1.ExpiresAt)
	}

	valid, err := a2.ValidateAccessToken(ctx, "sb-shared", tok1.Token)
	if err != nil || !valid {
		t.Fatalf("cross-instance validation: (%v,%v)", valid, err)
	}
	valid, err = a2.ValidateAccessToken(ctx, "sb-shared", "wrong")
	if err != nil || valid {
		t.Fatalf("wrong token validated: (%v,%v)", valid, err)
	}
}

func TestGetAccessTokenConcurrentMintSingleWinner(t *testing.T) {
	store := sessionstore.NewMemory()
	const n = 8
	tokens := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a := newTokenTestAdapter(store)
			tok, err := a.GetAccessToken(context.Background(), "sb-race")
			if err != nil {
				t.Errorf("goroutine %d: %v", i, err)
				return
			}
			tokens[i] = tok.Token
		}(i)
	}
	wg.Wait()
	for i, tk := range tokens {
		if tk != tokens[0] {
			t.Fatalf("goroutine %d token %q != %q — SetNX arbitration broken", i, tk, tokens[0])
		}
	}
}

func TestValidateAccessTokenMissingEntry(t *testing.T) {
	a := newTokenTestAdapter(sessionstore.NewMemory())
	// No entry: false, nil (not an error).
	ok, err := a.ValidateAccessToken(context.Background(), "sb-none", "tok")
	if err != nil || ok {
		t.Fatalf("missing entry: (%v,%v)", ok, err)
	}
}
