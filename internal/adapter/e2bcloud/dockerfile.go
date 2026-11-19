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
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/e2bgateway/e2bgateway/internal/api/dto"
)

// parseDockerfileBuild converts a Dockerfile into the E2B V2 build-start
// representation: FROM becomes fromImage and each supported instruction
// becomes a TemplateStepV2, preserving order. Instructions that cannot be
// expressed in a build-start request (COPY/ADD need uploaded file hashes;
// multi-stage builds need cross-stage copies; CMD/ENTRYPOINT and the rest
// have no step type) are rejected instead of being silently dropped — the
// caller must fail the rebuild BEFORE registering it upstream.
//
// Errors are returned as 400 APIErrors so the HTTP layer can surface them
// as client errors rather than backend failures.
func parseDockerfileBuild(dockerfile string) (string, []dto.TemplateStepV2, error) {
	var fromImage string
	var steps []dto.TemplateStepV2
	sawFrom := false

	lines, escape, err := splitDockerfileLines(dockerfile)
	if err != nil {
		return "", nil, err
	}
	for _, line := range lines {
		idx := strings.IndexAny(line, " \t")
		instr, arg := line, ""
		if idx >= 0 {
			instr, arg = line[:idx], line[idx+1:]
		}
		arg = strings.TrimSpace(arg)
		upper := strings.ToUpper(instr)

		if upper == "FROM" {
			if sawFrom {
				return "", nil, rejectBuildRequest("multi-stage Dockerfiles are not supported for rebuild: multiple FROM instructions")
			}
			image, err := parseFromArg(arg, line)
			if err != nil {
				return "", nil, err
			}
			fromImage, sawFrom = image, true
			continue
		}

		step, err := dockerfileStep(upper, arg, sawFrom, escape)
		if err != nil {
			return "", nil, err
		}
		steps = append(steps, step)
	}

	if !sawFrom {
		return "", nil, rejectBuildRequest("Dockerfile has no FROM instruction")
	}
	return fromImage, steps, nil
}

// rejectBuildRequest builds a 400 APIError for Dockerfiles the V2 build-start
// API cannot express.
func rejectBuildRequest(format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	return &APIError{StatusCode: http.StatusBadRequest, Code: http.StatusBadRequest, Message: msg}
}

const (
	dockerfileRun     = "RUN"
	dockerfileUser    = "USER"
	dockerfileWorkdir = "WORKDIR"
)

// dockerfileStep maps a single non-FROM instruction to a V2 build step.
func dockerfileStep(instr, arg string, sawFrom bool, escape rune) (dto.TemplateStepV2, error) {
	if !sawFrom {
		return dto.TemplateStepV2{}, rejectBuildRequest("%s instruction appears before FROM", instr)
	}
	if (instr == dockerfileRun || instr == dockerfileWorkdir || instr == dockerfileUser) && arg == "" {
		return dto.TemplateStepV2{}, rejectBuildRequest("%s instruction has no operand", instr)
	}
	switch instr {
	case dockerfileRun:
		var execArgs []string
		if strings.HasPrefix(arg, "[") && json.Unmarshal([]byte(arg), &execArgs) == nil {
			return dto.TemplateStepV2{}, rejectBuildRequest("RUN exec form is not supported for rebuild")
		}
		if strings.HasPrefix(arg, "--") {
			return dto.TemplateStepV2{}, rejectBuildRequest("RUN flags such as %q are not supported for rebuild", strings.Fields(arg)[0])
		}
		return dto.TemplateStepV2{Type: dockerfileRun, Args: []string{arg}}, nil
	case "ENV":
		pairs, err := parseEnvArg(arg, escape)
		if err != nil {
			return dto.TemplateStepV2{}, rejectBuildRequest("%v", err)
		}
		return dto.TemplateStepV2{Type: "ENV", Args: pairs}, nil
	case dockerfileWorkdir:
		return dto.TemplateStepV2{Type: dockerfileWorkdir, Args: []string{arg}}, nil
	case dockerfileUser:
		return dto.TemplateStepV2{Type: dockerfileUser, Args: []string{arg}}, nil
	case "COPY", "ADD":
		return dto.TemplateStepV2{}, rejectBuildRequest("%s is not supported for rebuild: file contents cannot be uploaded through this API; build the template with the E2B CLI instead", instr)
	default:
		return dto.TemplateStepV2{}, rejectBuildRequest("unsupported Dockerfile instruction %q for rebuild; supported: FROM, RUN, ENV, WORKDIR, USER", instr)
	}
}

var stageAlias = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// parseFromArg accepts a single image and an optional stage alias.
func parseFromArg(arg, line string) (string, error) {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return "", rejectBuildRequest("FROM instruction has no image: %q", line)
	}
	for _, f := range fields {
		if strings.HasPrefix(f, "--") {
			return "", rejectBuildRequest("FROM flags are not supported for rebuild: %q", f)
		}
	}
	validAlias := len(fields) == 3 && strings.EqualFold(fields[1], "AS") && stageAlias.MatchString(fields[2])
	if len(fields) != 1 && !validAlias {
		return "", rejectBuildRequest("unsupported FROM directive: %q", line)
	}
	return fields[0], nil
}

// parseEnvArg flattens an ENV argument list into the SDK's [k1, v1, k2, v2]
// wire format. Both the K=V form (possibly multiple, possibly quoted) and the
// legacy "ENV key value with spaces" form are accepted.
func parseEnvArg(arg string, escape rune) ([]string, error) {
	tokens, err := splitRespectingQuotes(arg, escape)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("ENV instruction has no arguments")
	}
	if !strings.Contains(tokens[0], "=") {
		// Legacy form: first token is the key, the rest is one value.
		if len(tokens) < 2 {
			return nil, fmt.Errorf("ENV instruction has no value: %q", arg)
		}
		return []string{tokens[0], strings.Join(tokens[1:], " ")}, nil
	}
	pairs := make([]string, 0, 2*len(tokens))
	for _, tok := range tokens {
		k, v, ok := strings.Cut(tok, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid ENV assignment %q", tok)
		}
		pairs = append(pairs, k, v)
	}
	return pairs, nil
}

// splitDockerfileLines normalizes a Dockerfile into logical lines: comments
// and blank lines are dropped and continuations use the declared escape character.
func splitDockerfileLines(dockerfile string) ([]string, rune, error) {
	var lines []string
	var pending strings.Builder
	directives := true
	escape := '\\'
	for raw := range strings.SplitSeq(dockerfile, "\n") {
		line := strings.TrimSpace(raw)
		if comment, ok := strings.CutPrefix(line, "#"); ok {
			if directives {
				key, value, ok := strings.Cut(strings.TrimSpace(comment), "=")
				if ok {
					switch strings.ToLower(strings.TrimSpace(key)) {
					case "escape":
						value = strings.TrimSpace(value)
						if value != "\\" && value != "`" {
							return nil, escape, rejectBuildRequest("invalid escape directive")
						}
						escape = rune(value[0])
						continue
					case "syntax":
						return nil, escape, rejectBuildRequest("syntax directives are not supported for rebuild")
					}
				}
			}
			directives = false
			continue
		}
		directives = false
		if line == "" {
			continue
		}
		if rest, ok := strings.CutSuffix(line, string(escape)); ok {
			pending.WriteString(strings.TrimSpace(rest))
			pending.WriteByte(' ')
			continue
		}
		pending.WriteString(line)
		lines = append(lines, strings.TrimSpace(pending.String()))
		pending.Reset()
	}
	if line := strings.TrimSpace(pending.String()); line != "" {
		lines = append(lines, line)
	}
	return lines, escape, nil
}

// splitRespectingQuotes applies Dockerfile quoting and backslash escaping.
func splitRespectingQuotes(s string, escape rune) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	var quote rune
	started, escaped := false, false
	runes := []rune(s)
	for i, r := range runes {
		if escaped {
			cur.WriteRune(r)
			escaped = false
			started = true
			continue
		}
		if r == escape && shouldEscapeEnv(quote, escape, nextEnvRune(runes, i)) {
			escaped = true
			started = true
			continue
		}
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
			started = true
		case r == '"' || r == '\'':
			quote = r
			started = true
		case r == ' ' || r == '\t':
			if started {
				tokens = append(tokens, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if quote != 0 || escaped {
		return nil, fmt.Errorf("unterminated ENV quote or escape")
	}
	if started {
		tokens = append(tokens, cur.String())
	}
	return tokens, nil
}

// shouldEscapeEnv follows Dockerfile escaping inside and outside quoted values.
func shouldEscapeEnv(quote, escape, next rune) bool {
	switch quote {
	case '\'':
		return false
	case '"':
		return next == escape || next == '"' || next == '$'
	default:
		return true
	}
}

func nextEnvRune(runes []rune, i int) rune {
	if i+1 < len(runes) {
		return runes[i+1]
	}
	return 0
}
