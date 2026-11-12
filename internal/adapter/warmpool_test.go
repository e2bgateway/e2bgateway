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

package adapter

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateWarmPoolID(t *testing.T) {
	valid := []string{"a", "wp-base-1a2b3c4d", "pool-0", "x" + strings.Repeat("-y", 31)}
	for _, id := range valid {
		if err := ValidateWarmPoolID(id); err != nil {
			t.Errorf("ValidateWarmPoolID(%q) = %v, want nil", id, err)
		}
	}
	invalid := []string{
		"",                      // empty
		"-lead",                 // leading dash
		"trail-",                // trailing dash
		"Upper",                 // uppercase
		"with space",            // space
		"brace{id}",             // Redis key injection chars
		strings.Repeat("a", 64), // too long
	}
	for _, id := range invalid {
		if err := ValidateWarmPoolID(id); err == nil {
			t.Errorf("ValidateWarmPoolID(%q) = nil, want error", id)
		}
	}
}

func TestGenerateWarmPoolID(t *testing.T) {
	id := GenerateWarmPoolID("My_Template!v2")
	if err := ValidateWarmPoolID(id); err != nil {
		t.Fatalf("GenerateWarmPoolID produced invalid ID %q: %v", id, err)
	}
	if !strings.HasPrefix(id, "wp-") {
		t.Errorf("GenerateWarmPoolID(%q) = %q, want prefix wp-", "My_Template!v2", id)
	}
	// Deterministic: same input, same output.
	if id != GenerateWarmPoolID("My_Template!v2") {
		t.Error("GenerateWarmPoolID is not deterministic")
	}
	// Different inputs produce different IDs.
	if id == GenerateWarmPoolID("other-template") {
		t.Error("GenerateWarmPoolID collided across different templates")
	}
	// Long template IDs stay within the 63-char limit.
	if long := GenerateWarmPoolID(strings.Repeat("t", 200)); len(long) > 63 {
		t.Errorf("GenerateWarmPoolID(long) len = %d, want <= 63", len(long))
	}
	if err := ValidateWarmPoolID(GenerateWarmPoolID(strings.Repeat("t", 200))); err != nil {
		t.Errorf("GenerateWarmPoolID(long) invalid: %v", err)
	}
	// Empty-ish template ID still yields a valid ID.
	if err := ValidateWarmPoolID(GenerateWarmPoolID("!!!")); err != nil {
		t.Errorf("GenerateWarmPoolID(!!!) invalid: %v", err)
	}
}

func TestClampTargetSize(t *testing.T) {
	tests := []struct {
		name    string
		n, max  int
		want    int
		wantErr bool
	}{
		{"normal", 5, 256, 5, false},
		{"zero", 0, 256, 0, false},
		{"at max", 256, 256, 256, false},
		{"above max clamps", 1000, 256, 256, false},
		{"negative errors", -1, 256, 0, true},
		{"non-positive max uses default", 300, 0, 256, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ClampTargetSize(tt.n, tt.max)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ClampTargetSize(%d, %d) err = %v, wantErr %v", tt.n, tt.max, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ClampTargetSize(%d, %d) = %d, want %d", tt.n, tt.max, got, tt.want)
			}
		})
	}
}

func TestSentinelErrorsDistinct(t *testing.T) {
	if errors.Is(ErrWarmPoolNotFound, ErrWarmPoolAlreadyExists) {
		t.Error("sentinel errors must be distinct")
	}
}
