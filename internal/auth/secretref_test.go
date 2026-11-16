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

package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e2bgateway/e2bgateway/internal/config"
)

// writeKeyFile writes content to a keys.json file in a fresh temp dir and
// returns its path.
func writeKeyFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing key file: %v", err)
	}
	return path
}

func TestLoadKeysFromSecretFile_Valid(t *testing.T) {
	path := writeKeyFile(t, `{
		"sk-prod-abc": {"tenantID": "acme", "apiKeyID": "key-1", "scopes": ["sandbox:read"], "metadata": {"team": "infra"}},
		"sk-no-scopes": {"tenantID": "solo"}
	}`)

	keys, err := loadKeysFromSecretFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}

	tc := keys["sk-prod-abc"]
	if tc.TenantID != "acme" || tc.APIKeyID != "key-1" {
		t.Errorf("unexpected tenant context: %+v", tc)
	}
	if len(tc.Scopes) != 1 || tc.Scopes[0] != "sandbox:read" {
		t.Errorf("unexpected scopes: %v", tc.Scopes)
	}
	if tc.Metadata["team"] != "infra" {
		t.Errorf("unexpected metadata: %v", tc.Metadata)
	}

	// Omitted scopes must grant nothing (safe default).
	if HasScope(keys["sk-no-scopes"], "sandbox:read") {
		t.Error("expected no scope grants when scopes omitted")
	}
}

func TestLoadKeysFromSecretFile_Errors(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"invalid JSON", `{not json`},
		{"empty object", `{}`},
		{"JSON array", `[{"tenantID": "a"}]`},
		{"null", `null`},
		{"missing tenantID", `{"sk-1": {"scopes": ["*"]}}`},
		{"empty tenantID", `{"sk-1": {"tenantID": ""}}`},
		{"string value", `{"sk-1": "just-a-string"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeKeyFile(t, tt.content)
			if _, err := loadKeysFromSecretFile(path); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestLoadKeysFromSecretFile_MissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.json")
	if _, err := loadKeysFromSecretFile(missing); err == nil {
		t.Error("expected error for missing file")
	}
}

// Errors must not leak secret material: the JSON map keys ARE the API keys.
func TestLoadKeysFromSecretFile_NoSecretMaterialInError(t *testing.T) {
	path := writeKeyFile(t, `{"sk-super-secret-value": {"scopes": ["*"]}}`)
	_, err := loadKeysFromSecretFile(path)
	if err == nil {
		t.Fatal("expected error (missing tenantID)")
	}
	if strings.Contains(err.Error(), "sk-super-secret-value") {
		t.Errorf("error must not contain key material, got: %v", err)
	}
	if strings.Contains(err.Error(), "sandbox") {
		t.Errorf("error must not contain file content, got: %v", err)
	}
}

func TestNewAPIKeyProvider_SecretRef(t *testing.T) {
	path := writeKeyFile(t, `{"sk-file-key": {"tenantID": "file-tenant", "apiKeyID": "key-f", "scopes": ["sandbox:read"]}}`)
	p, err := NewAPIKeyProvider(config.AuthProviderConfig{
		Type:       "apikey",
		SecretRef:  path,
		HeaderName: "X-API-Key",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "sk-file-key")
	tc, err := p.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if tc.TenantID != "file-tenant" || tc.APIKeyID != "key-f" {
		t.Errorf("unexpected tenant context: %+v", tc)
	}
	if !HasScope(tc, "sandbox:read") {
		t.Error("expected sandbox:read scope from file")
	}
}

// Fail fast: secretRef set but unresolvable must be a startup error.
func TestNewManager_SecretRefUnresolvable(t *testing.T) {
	_, err := NewManager(config.AuthConfig{
		Providers: []config.AuthProviderConfig{
			{Type: "apikey", SecretRef: "/nonexistent/keys.json"},
		},
	})
	if err == nil {
		t.Fatal("expected error for unresolvable secretRef")
	}
}

// Regression for #92: secretRef must never enable the legacy hardcoded keys.
func TestAPIKeyProvider_NoHardcodedFallback(t *testing.T) {
	path := writeKeyFile(t, `{"sk-real": {"tenantID": "real-tenant"}}`)
	p, err := NewAPIKeyProvider(config.AuthProviderConfig{
		Type:      "apikey",
		SecretRef: path,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, legacy := range []string{"test-key-1", "test-key-admin"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-API-Key", legacy)
		if _, err := p.Authenticate(req); err == nil {
			t.Errorf("legacy hardcoded key %q must not be accepted", legacy)
		}
	}
}
