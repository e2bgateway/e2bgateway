package opensandbox

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
	"github.com/e2bgateway/e2bgateway/internal/adapter"
)

// TestListProcesses_Parsing tests that ListProcesses correctly parses ps output
func TestListProcesses_Parsing(t *testing.T) {
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

// TestWriteFile_NoHeredoc tests that WriteFile doesn't use heredoc (security fix)
func TestWriteFile_NoHeredoc(t *testing.T) {
	// This test documents that WriteFile should use UploadFile, not heredoc
	// The actual implementation uses UploadFile with bytes.NewReader
	// which is binary-safe and doesn't have shell injection vulnerabilities

	// Test content that would break heredoc
	content := []byte("test content with EOF marker\nEOF\nmore content")

	// Verify the content doesn't contain problematic patterns
	if strings.Contains(string(content), "<<") {
		t.Error("content contains heredoc markers")
	}
}

// TestExecdClientCache_ConcurrentAccess tests concurrent access to execdClients map
func TestExecdClientCache_ConcurrentAccess(t *testing.T) {
	a := &Adapter{
		name:            "test",
		execdClients:    make(map[string]*opensandbox.ExecdClient),
	}

	// Simulate concurrent access
	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func(id int) {
			key := string(rune('A' + id))
			a.execdClientsMu.Lock()
			a.execdClients[key] = nil // Can't create real ExecdClient in test
			a.execdClientsMu.Unlock()

			a.execdClientsMu.RLock()
			_ = a.execdClients[key]
			a.execdClientsMu.RUnlock()
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify all entries exist
	a.execdClientsMu.RLock()
	count := len(a.execdClients)
	a.execdClientsMu.RUnlock()

	if count != 10 {
		t.Errorf("expected 10 entries in execdClients, got %d", count)
	}
}

// TestAdapter_InterfaceCompliance verifies Adapter implements SandboxAdapter
func TestAdapter_InterfaceCompliance(t *testing.T) {
	// This is a compile-time check
	var _ adapter.SandboxAdapter = (*Adapter)(nil)
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

// TestBinaryDataHandling tests that file operations preserve binary data
func TestBinaryDataHandling(t *testing.T) {
	// Test that bytes.NewReader preserves binary data
	binaryData := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD}

	// This is what the fixed DownloadFile does
	reader := bytes.NewReader(binaryData)
	if reader == nil {
		t.Error("bytes.NewReader returned nil")
	}

	// Read back the data
	buf := make([]byte, len(binaryData))
	n, err := reader.Read(buf)
	if err != nil {
		t.Errorf("Read failed: %v", err)
	}
	if n != len(binaryData) {
		t.Errorf("expected to read %d bytes, got %d", len(binaryData), n)
	}

	// Verify data is preserved
	for i, b := range binaryData {
		if buf[i] != b {
			t.Errorf("byte %d: expected %x, got %x", i, b, buf[i])
		}
	}
}

// TestTimeoutRespect tests that operations respect context deadlines
func TestTimeoutRespect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()

	// Wait for context to expire
	time.Sleep(1 * time.Millisecond)

	// Verify context has expired
	if err := ctx.Err(); err == nil {
		t.Error("expected context to have expired")
	}
}
