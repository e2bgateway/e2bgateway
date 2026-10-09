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

package util

import (
	"fmt"
	"strings"
)

// ShellQuote safely quotes a string for use in shell commands.
// It wraps the string in single quotes and escapes any embedded single quotes.
func ShellQuote(s string) string {
	// Replace ' with '\'' (end quote, escaped quote, start quote)
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// WrapCodeInCommand wraps code in a language-specific execution command.
func WrapCodeInCommand(code string, language string) string {
	switch strings.ToLower(language) {
	case "python", "python3", "":
		return fmt.Sprintf("python3 -c %q", code)
	case "javascript", "node":
		return fmt.Sprintf("node -e %q", code)
	case "bash", "sh":
		return code
	default:
		return fmt.Sprintf("%s -c %q", language, code)
	}
}

// ValidatePID validates that a process ID is a pure number.
// Returns the parsed PID or an error if the input is invalid.
func ValidatePID(processID string) (int, error) {
	var pid int
	var extra string
	n, err := fmt.Sscanf(processID, "%d%s", &pid, &extra)
	if n != 1 || (err != nil && err.Error() != "EOF") {
		return 0, fmt.Errorf("invalid process ID: must be numeric, got %q", processID)
	}
	return pid, nil
}
