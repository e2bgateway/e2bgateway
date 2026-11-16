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

package adapter_test

import (
	"testing"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/sessionstore"
)

func TestSandboxBackendMapCountByBackend(t *testing.T) {
	m := adapter.NewSandboxBackendMap(sessionstore.NewMemory(), time.Hour)
	for _, e := range []struct{ sb, backend string }{
		{"sb1", "agent-sandbox"},
		{"sb2", "agent-sandbox"},
		{"sb3", "opensandbox"},
	} {
		if err := m.Set(e.sb, e.backend); err != nil {
			t.Fatalf("Set(%s): %v", e.sb, err)
		}
	}
	got := m.CountByBackend()
	if got["agent-sandbox"] != 2 || got["opensandbox"] != 1 || len(got) != 2 {
		t.Fatalf("CountByBackend() = %v", got)
	}
}
