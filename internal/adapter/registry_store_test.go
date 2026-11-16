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
	"testing"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/sessionstore"
)

func TestSandboxBackendMapStoreBacked(t *testing.T) {
	store := sessionstore.NewMemory()
	m := NewSandboxBackendMap(store, time.Hour)

	if err := m.Set("sb1", "agentsandbox"); err != nil {
		t.Fatal(err)
	}
	name, ok, err := m.Get("sb1")
	if err != nil || !ok || name != "agentsandbox" {
		t.Fatalf("Get: (%q,%v,%v)", name, ok, err)
	}

	// A second map over the same store sees the entry (cross-replica).
	m2 := NewSandboxBackendMap(store, time.Hour)
	if name, ok, _ := m2.Get("sb1"); !ok || name != "agentsandbox" {
		t.Fatalf("second replica Get: (%q,%v)", name, ok)
	}

	if _, ok, _ := m.Get("missing"); ok {
		t.Error("missing sandbox must not be found")
	}

	if err := m.Set("sb2", "opensandbox"); err != nil {
		t.Fatal(err)
	}
	counts := m.CountByBackend()
	if counts["agentsandbox"] != 1 || counts["opensandbox"] != 1 {
		t.Errorf("CountByBackend: %v", counts)
	}
	if m.Size() != 2 {
		t.Errorf("Size: %d, want 2", m.Size())
	}

	if err := m.Delete("sb1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := m.Get("sb1"); ok {
		t.Error("deleted sandbox still found")
	}
}
