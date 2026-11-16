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
	"context"
	"strings"
	"testing"

	"github.com/e2bgateway/e2bgateway/internal/sessionstore"
)

func TestGetAccessTokenSharedStore(t *testing.T) {
	store := sessionstore.NewMemory()
	a1 := &Adapter{name: "osbx-a", useSignedEndpoint: false, sessionStore: store}
	a2 := &Adapter{name: "osbx-b", useSignedEndpoint: false, sessionStore: store}
	ctx := context.Background()

	tok1, err := a1.GetAccessToken(ctx, "sb-os")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok1.Token, "envd_sb-os_") {
		t.Errorf("unexpected token format: %q", tok1.Token)
	}
	tok2, err := a2.GetAccessToken(ctx, "sb-os")
	if err != nil {
		t.Fatal(err)
	}
	if tok2.Token != tok1.Token {
		t.Fatalf("second instance minted different token: %q vs %q", tok2.Token, tok1.Token)
	}
	// ExpiresAt must be the stored expiry, not re-stamped.
	if tok2.ExpiresAt.After(tok1.ExpiresAt) {
		t.Errorf("ExpiresAt was re-stamped: %v > %v", tok2.ExpiresAt, tok1.ExpiresAt)
	}
	valid, err := a2.ValidateAccessToken(ctx, "sb-os", tok1.Token)
	if err != nil || !valid {
		t.Fatalf("cross-instance validation: (%v,%v)", valid, err)
	}
	valid, err = a2.ValidateAccessToken(ctx, "sb-os", "wrong")
	if err != nil || valid {
		t.Fatalf("wrong token validated: (%v,%v)", valid, err)
	}
}

func TestValidateAccessTokenMissingEntry(t *testing.T) {
	a := &Adapter{name: "osbx", sessionStore: sessionstore.NewMemory()}
	ok, err := a.ValidateAccessToken(context.Background(), "sb-none", "tok")
	if err != nil || ok {
		t.Fatalf("missing entry: (%v,%v)", ok, err)
	}
}
