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
	"fmt"
	"net/http"
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

	for _, line := range splitDockerfileLines(dockerfile) {
		instr, arg, _ := strings.Cut(line, " ")
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

		step, err := dockerfileStep(upper, arg, sawFrom)
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

// dockerfileStep maps a single non-FROM instruction to a V2 build step.
func dockerfileStep(instr, arg string, sawFrom bool) (dto.TemplateStepV2, error) {
	if !sawFrom {
		return dto.TemplateStepV2{}, rejectBuildRequest("%s instruction appears before FROM", instr)
	}
	switch instr {
	case "RUN":
		if strings.HasPrefix(arg, "--") {
			return dto.TemplateStepV2{}, rejectBuildRequest("RUN flags such as %q are not supported for rebuild", strings.Fields(arg)[0])
		}
		return dto.TemplateStepV2{Type: "RUN", Args: []string{arg}}, nil
	case "ENV":
		pairs, err := parseEnvArg(arg)
		if err != nil {
			return dto.TemplateStepV2{}, rejectBuildRequest("%v", err)
		}
		return dto.TemplateStepV2{Type: "ENV", Args: pairs}, nil
	case "WORKDIR":
		return dto.TemplateStepV2{Type: "WORKDIR", Args: []string{arg}}, nil
	case "USER":
		return dto.TemplateStepV2{Type: "USER", Args: []string{arg}}, nil
	case "COPY", "ADD":
		return dto.TemplateStepV2{}, rejectBuildRequest("%s is not supported for rebuild: file contents cannot be uploaded through this API; build the template with the E2B CLI instead", instr)
	default:
		return dto.TemplateStepV2{}, rejectBuildRequest("unsupported Dockerfile instruction %q for rebuild; supported: FROM, RUN, ENV, WORKDIR, USER", instr)
	}
}

// parseFromArg extracts the image from a full FROM line, skipping --platform
// style flags. Stage aliases ("AS <name>") are rejected: they imply a
// multi-stage build whose cross-stage copies cannot be expressed here.
func parseFromArg(arg, line string) (string, error) {
	image := ""
	for f := range strings.FieldsSeq(arg) {
		switch {
		case strings.HasPrefix(f, "--"):
			// platform and similar flags: not representable, ignore
		case strings.EqualFold(f, "AS"):
			return "", rejectBuildRequest("FROM ... AS (stage alias) implies a multi-stage build, which is not supported for rebuild: %q", line)
		case image == "":
			image = f
		default:
			return "", rejectBuildRequest("unsupported FROM directive: %q", line)
		}
	}
	if image == "" {
		return "", rejectBuildRequest("FROM instruction has no image: %q", line)
	}
	return image, nil
}

// parseEnvArg flattens an ENV argument list into the SDK's [k1, v1, k2, v2]
// wire format. Both the K=V form (possibly multiple, possibly quoted) and the
// legacy "ENV key value with spaces" form are accepted.
func parseEnvArg(arg string) ([]string, error) {
	tokens := splitRespectingQuotes(arg)
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
// and blank lines are dropped and backslash continuations are joined.
func splitDockerfileLines(dockerfile string) []string {
	var lines []string
	var pending string
	for raw := range strings.SplitSeq(dockerfile, "\n") {
		line := strings.TrimSpace(raw)
		if pending == "" && (line == "" || strings.HasPrefix(line, "#")) {
			continue
		}
		if rest, ok := strings.CutSuffix(line, "\\"); ok {
			pending += strings.TrimSpace(rest) + " "
			continue
		}
		pending += line
		lines = append(lines, strings.TrimSpace(pending))
		pending = ""
	}
	if pending = strings.TrimSpace(pending); pending != "" {
		lines = append(lines, pending)
	}
	return lines
}

// splitRespectingQuotes splits on whitespace but keeps double-quoted spans
// together (quotes are removed), matching how Docker tokenizes ENV values.
func splitRespectingQuotes(s string) []string {
	var tokens []string
	var cur strings.Builder
	inQuotes := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuotes = !inQuotes
		case (r == ' ' || r == '\t') && !inQuotes:
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}
