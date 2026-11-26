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

package cmd

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

func TestVersionCommandOutput(t *testing.T) {
	cmd := newVersionCommand("v9.9.9", "abc1234", "2026-11-26T00:00:00Z")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute version command: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"v9.9.9", "abc1234", "2026-11-26T00:00:00Z", runtime.Version()} {
		if !strings.Contains(out, want) {
			t.Errorf("version output %q missing %q", out, want)
		}
	}
}