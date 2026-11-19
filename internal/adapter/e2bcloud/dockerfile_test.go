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

package e2bcloud

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/e2bgateway/e2bgateway/internal/api/dto"
)

func TestParseDockerfileBuild_PreservesSupportedSyntax(t *testing.T) {
	for _, from := range []string{"FROM base", "FROM\tbase AS build"} {
		input := from + "\nRUN\t[ -f /tmp/file ] && echo ok\nRUN echo first \\\n# comment\n  second\nENV A='hello world' B=hello\\ world C=\"escaped \\\"quote\\\"\" D=\\$HOME\nWORKDIR\t/app\nUSER\troot"
		image, steps, err := parseDockerfileBuild(input)
		require.NoError(t, err)
		require.Equal(t, "base", image)
		require.Equal(t, []dto.TemplateStepV2{
			{Type: "RUN", Args: []string{"[ -f /tmp/file ] && echo ok"}},
			{Type: "RUN", Args: []string{"echo first second"}},
			{Type: "ENV", Args: []string{"A", "hello world", "B", "hello world", "C", `escaped "quote"`, "D", "$HOME"}},
			{Type: "WORKDIR", Args: []string{"/app"}},
			{Type: "USER", Args: []string{"root"}},
		}, steps)
	}
}

func TestParseDockerfileBuild_EscapeDirective(t *testing.T) {
	image, steps, err := parseDockerfileBuild("# escape=`\nFROM base\nRUN echo one `\n# comment\ntwo\nENV A=hello` world")
	require.NoError(t, err)
	require.Equal(t, "base", image)
	require.Equal(t, []dto.TemplateStepV2{
		{Type: "RUN", Args: []string{"echo one two"}},
		{Type: "ENV", Args: []string{"A", "hello world"}},
	}, steps)
}
