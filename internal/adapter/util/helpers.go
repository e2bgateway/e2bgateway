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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// GenerateTemplateID produces a DNS-1123-safe template ID from a user-supplied
// name. The name is lowercased, non-alphanumeric characters are replaced with
// hyphens, leading/trailing hyphens are trimmed, and a random 12-hex-char
// suffix is appended for uniqueness. Returns an error if the random source
// fails.
func GenerateTemplateID(name string) (string, error) {
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return '-'
	}, name)
	sanitized = strings.Trim(sanitized, "-")
	if sanitized == "" {
		sanitized = "template"
	}
	suffix, err := GenerateE2BID()
	if err != nil {
		return "", err
	}
	return sanitized + "-" + suffix, nil
}

// MustGenerateTemplateID is GenerateTemplateID but panics on crypto/rand
// failure (entropy exhaustion). Callers in adapter methods should not
// encounter this in practice; use it to keep call sites uncluttered when
// the error is truly unrecoverable.
func MustGenerateTemplateID(name string) string {
	id, err := GenerateTemplateID(name)
	if err != nil {
		panic(err)
	}
	return id
}

// ParseDockerfileFrom extracts the runtime image URI from a Dockerfile's last
// FROM directive. In multi-stage Dockerfiles the final FROM is the runtime
// image (earlier stages are build-only); for single-stage Dockerfiles this is
// the only FROM. Returns empty string if no valid FROM is found.
func ParseDockerfileFrom(dockerfile string) string {
	lastFrom := ""
	for line := range strings.SplitSeq(dockerfile, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(line), "FROM ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				lastFrom = parts[1]
			}
		}
	}
	return lastFrom
}

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

// GenerateE2BID generates an E2B-compatible ID (12 hex chars) using
// crypto/rand. Returns an error if the random source fails.
func GenerateE2BID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating E2B ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// MustGenerateE2BID is GenerateE2BID but panics on crypto/rand failure
// (entropy exhaustion). Callers in adapter methods should not encounter
// this in practice; use it to keep call sites uncluttered when the error
// is truly unrecoverable.
func MustGenerateE2BID() string {
	id, err := GenerateE2BID()
	if err != nil {
		panic(err)
	}
	return id
}
