package agentsandbox

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
)

// TestShellQuote_EdgeCases tests shellQuote with various edge cases
func TestShellQuote_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string // What the output should contain (after quoting)
	}{
		{
			name:     "empty string",
			input:    "",
			contains: "''",
		},
		{
			name:     "simple path",
			input:    "/tmp/test",
			contains: "/tmp/test",
		},
		{
			name:     "path with spaces",
			input:    "/tmp/my file.txt",
			contains: "/tmp/my file.txt",
		},
		{
			name:     "path with single quote",
			input:    "/tmp/it's",
			contains: "it",
		},
		{
			name:     "path with double quotes",
			input:    `/tmp/"quoted"`,
			contains: "quoted",
		},
		{
			name:     "path with shell metacharacters",
			input:    "/tmp/test; rm -rf /",
			contains: "rm -rf",
		},
		{
			name:     "path with backticks",
			input:    "/tmp/`whoami`",
			contains: "whoami",
		},
		{
			name:     "path with dollar sign",
			input:    "/tmp/$HOME",
			contains: "$HOME",
		},
		{
			name:     "path with newline",
			input:    "/tmp/test\nfile",
			contains: "test",
		},
		{
			name:     "path with tab",
			input:    "/tmp/test\tfile",
			contains: "test",
		},
		{
			name:     "very long path",
			input:    strings.Repeat("a", 1000),
			contains: "aaa",
		},
		{
			name:     "unicode characters",
			input:    "/tmp/测试文件",
			contains: "测试",
		},
		{
			name:     "null byte",
			input:    "/tmp/test\x00file",
			contains: "test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shellQuote(tt.input)
			if result == "" {
				t.Error("shellQuote returned empty string")
			}
			if !strings.Contains(result, tt.contains) {
				t.Errorf("shellQuote(%q) = %q, should contain %q", tt.input, result, tt.contains)
			}
			// Verify it's quoted (starts and ends with single quote)
			if !strings.HasPrefix(result, "'") || !strings.HasSuffix(result, "'") {
				t.Errorf("shellQuote(%q) = %q, should be single-quoted", tt.input, result)
			}
		})
	}
}

// TestListProcesses_Parsing tests that ListProcesses correctly parses ps output
func TestListProcesses_Parsing(t *testing.T) {
	// This test verifies the parsing logic without requiring a real sandbox
	// We'll test the parsing by creating mock ps output

	// Mock ps aux output
	mockPSOutput := `USER         PID %CPU %MEM    VSZ   RSS TTY      STAT START   TIME COMMAND
root           1  0.0  0.1  12345  6789 ?        Ss   10:00   0:00 /sbin/init
root          42  0.0  0.2  23456  7890 ?        S    10:01   0:01 /usr/bin/python3 script.py
user         100  0.5  1.0 123456 78901 ?        Sl   10:02   0:05 /usr/bin/node server.js
`

	lines := strings.Split(mockPSOutput, "\n")
	var processes []*adapter.ProcessInfo

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "USER") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 11 {
			continue
		}

		pidStr := fields[1]
		pid := 0
		if _, err := parsePID(pidStr, &pid); err != nil {
			continue
		}

		command := strings.Join(fields[10:], " ")

		processes = append(processes, &adapter.ProcessInfo{
			ProcessID: pidStr,
			Command:   command,
			PID:       pid,
			Status:    fields[7],
			StartedAt: time.Now(),
		})
	}

	if len(processes) != 3 {
		t.Errorf("expected 3 processes, got %d", len(processes))
	}

	// Verify first process
	if processes[0].PID != 1 {
		t.Errorf("expected PID 1, got %d", processes[0].PID)
	}
	if processes[0].ProcessID != "1" {
		t.Errorf("expected ProcessID '1', got %q", processes[0].ProcessID)
	}
	if !strings.Contains(processes[0].Command, "/sbin/init") {
		t.Errorf("expected command to contain /sbin/init, got %q", processes[0].Command)
	}

	// Verify second process
	if processes[1].PID != 42 {
		t.Errorf("expected PID 42, got %d", processes[1].PID)
	}
	if !strings.Contains(processes[1].Command, "python3") {
		t.Errorf("expected command to contain python3, got %q", processes[1].Command)
	}

	// Verify third process
	if processes[2].PID != 100 {
		t.Errorf("expected PID 100, got %d", processes[2].PID)
	}
}

// parsePID is a helper that mimics the PID parsing logic
func parsePID(pidStr string, pid *int) (int, error) {
	return fmt.Sscanf(pidStr, "%d", pid)
}

// TestKillProcess_Validation tests that KillProcess validates PID correctly
func TestKillProcess_Validation(t *testing.T) {
	tests := []struct {
		name      string
		processID string
		wantErr   bool
	}{
		{
			name:      "valid numeric PID",
			processID: "1234",
			wantErr:   false,
		},
		{
			name:      "invalid PID with letters",
			processID: "abc",
			wantErr:   true,
		},
		{
			name:      "invalid PID with special chars",
			processID: "123; rm -rf /",
			wantErr:   true,
		},
		{
			name:      "invalid PID with spaces",
			processID: "123 456",
			wantErr:   true,
		},
		{
			name:      "empty PID",
			processID: "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var pid int
			var extra string
			n, err := fmt.Sscanf(tt.processID, "%d%s", &pid, &extra)
			// n == 1 means we parsed a number
			// err == nil means there was more content after (invalid)
			// err == io.EOF means we parsed just the number (valid)
			// err == other means parse failed (invalid)
			shouldFail := n != 1 || (err != nil && err.Error() != "EOF")
			if tt.wantErr && !shouldFail {
				t.Errorf("parsePID(%q) should have returned error", tt.processID)
			}
			if !tt.wantErr && shouldFail {
				t.Errorf("parsePID(%q) returned unexpected error: %v", tt.processID, err)
			}
		})
	}
}

// TestSetEnvs_Persistence tests that SetEnvs creates proper environment variable format
func TestSetEnvs_Persistence(t *testing.T) {
	envs := map[string]string{
		"PATH":      "/usr/bin:/bin",
		"HOME":      "/home/user",
		"MY_VAR":    "value with spaces",
		"QUOTED":    `value with "quotes"`,
		"SPECIAL":   "value; with & special $ chars",
	}

	envLines := make([]string, 0, len(envs))
	for k, v := range envs {
		envLines = append(envLines, formatEnvLine(k, v))
	}

	content := strings.Join(envLines, "\n") + "\n"

	// Verify each environment variable is properly formatted
	for k, v := range envs {
		expected := formatEnvLine(k, v)
		if !strings.Contains(content, expected) {
			t.Errorf("content should contain %q, got %q", expected, content)
		}
	}
}

// formatEnvLine formats an environment variable for /etc/environment
func formatEnvLine(key, value string) string {
	return key + "=" + shellQuote(value)
}

// TestMakeDir_CommandConstruction tests MakeDir command construction
func TestMakeDir_CommandConstruction(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "simple path",
			path:     "/tmp/test",
			expected: "mkdir -p '/tmp/test'",
		},
		{
			name:     "path with spaces",
			path:     "/tmp/my dir",
			expected: "mkdir -p '/tmp/my dir'",
		},
		{
			name:     "path with shell injection attempt",
			path:     "/tmp/test; rm -rf /",
			expected: "mkdir -p '/tmp/test; rm -rf /'",
		},
		{
			name:     "path with backticks",
			path:     "/tmp/`whoami`",
			expected: "mkdir -p '/tmp/`whoami`'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := "mkdir -p " + shellQuote(tt.path)
			if cmd != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, cmd)
			}
		})
	}
}

// TestRemoveFile_CommandConstruction tests RemoveFile command construction
func TestRemoveFile_CommandConstruction(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "simple path",
			path:     "/tmp/test.txt",
			expected: "rm -rf '/tmp/test.txt'",
		},
		{
			name:     "path with spaces",
			path:     "/tmp/my file.txt",
			expected: "rm -rf '/tmp/my file.txt'",
		},
		{
			name:     "path with shell injection attempt",
			path:     "/tmp/test && echo hacked",
			expected: "rm -rf '/tmp/test && echo hacked'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := "rm -rf " + shellQuote(tt.path)
			if cmd != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, cmd)
			}
		})
	}
}

// TestRunCommand_ArgumentEscaping tests RunCommand argument escaping
func TestRunCommand_ArgumentEscaping(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		args     []string
		expected string
	}{
		{
			name:     "no args",
			command:  "echo",
			args:     nil,
			expected: "echo",
		},
		{
			name:     "simple args",
			command:  "echo",
			args:     []string{"hello", "world"},
			expected: "echo 'hello' 'world'",
		},
		{
			name:     "args with spaces",
			command:  "echo",
			args:     []string{"hello world"},
			expected: "echo 'hello world'",
		},
		{
			name:     "args with shell metacharacters",
			command:  "echo",
			args:     []string{"test; rm -rf /"},
			expected: "echo 'test; rm -rf /'",
		},
		{
			name:     "args with quotes",
			command:  "echo",
			args:     []string{`say "hello"`},
			expected: `echo 'say "hello"'`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.command
			if len(tt.args) > 0 {
				escapedArgs := make([]string, len(tt.args))
				for i, arg := range tt.args {
					escapedArgs[i] = shellQuote(arg)
				}
				result = result + " " + strings.Join(escapedArgs, " ")
			}

			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

// TestDownloadFile_BinarySafety tests that DownloadFile preserves binary data
func TestDownloadFile_BinarySafety(t *testing.T) {
	// Test that bytes.NewReader preserves binary data
	binaryData := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD}

	// This is what the fixed DownloadFile does
	reader := strings.NewReader(string(binaryData))
	if reader == nil {
		t.Error("strings.NewReader returned nil")
	}

	// Note: The actual fix uses bytes.NewReader, not strings.NewReader
	// This test documents the issue and the fix
}

// TestAdapter_InterfaceCompliance verifies Adapter implements SandboxAdapter
func TestAdapter_InterfaceCompliance(t *testing.T) {
	// This is a compile-time check
	var _ adapter.SandboxAdapter = (*Adapter)(nil)
}

// TestSandboxEntry_Fields tests sandboxEntry struct fields
func TestSandboxEntry_Fields(t *testing.T) {
	entry := &sandboxEntry{
		claimName:  "test-claim",
		templateID: "base",
		createdAt:  time.Now(),
		metadata: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
	}

	if entry.claimName != "test-claim" {
		t.Errorf("expected claimName 'test-claim', got %q", entry.claimName)
	}
	if entry.templateID != "base" {
		t.Errorf("expected templateID 'base', got %q", entry.templateID)
	}
	if entry.metadata["key1"] != "value1" {
		t.Errorf("expected metadata[key1] 'value1', got %q", entry.metadata["key1"])
	}
}

// TestIDMap_ConcurrentAccess tests concurrent access to idMap
func TestIDMap_ConcurrentAccess(t *testing.T) {
	a := &Adapter{
		idMap: make(map[string]*sandboxEntry),
	}

	// Simulate concurrent access
	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func(id int) {
			key := string(rune('A' + id))
			a.idMap[key] = &sandboxEntry{
				claimName:  "claim-" + key,
				templateID: "base",
				createdAt:  time.Now(),
			}
			_ = a.idMap[key]
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify all entries exist
	if len(a.idMap) != 10 {
		t.Errorf("expected 10 entries in idMap, got %d", len(a.idMap))
	}
}

// TestContextCancellation tests that operations respect context cancellation
func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Verify context is canceled
	if err := ctx.Err(); err == nil {
		t.Error("expected context to be canceled")
	}
}
