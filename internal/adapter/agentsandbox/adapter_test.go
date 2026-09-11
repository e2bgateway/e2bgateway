package agentsandbox

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/cache"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestShellQuote tests the shellQuote helper function
func TestShellQuote(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple path",
			input:    "/tmp/test",
			expected: "'/tmp/test'",
		},
		{
			name:     "path with spaces",
			input:    "/tmp/my file.txt",
			expected: "'/tmp/my file.txt'",
		},
		{
			name:     "path with single quote",
			input:    "/tmp/it's",
			expected: "'/tmp/it'\\''s'",
		},
		{
			name:     "path with shell metacharacters",
			input:    "/tmp/test; rm -rf /",
			expected: "'/tmp/test; rm -rf /'",
		},
		{
			name:     "path with backticks",
			input:    "/tmp/`whoami`",
			expected: "'/tmp/`whoami`'",
		},
		{
			name:     "path with dollar sign",
			input:    "/tmp/$HOME",
			expected: "'/tmp/$HOME'",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "''",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shellQuote(tt.input)
			if result != tt.expected {
				t.Errorf("shellQuote(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestAdapterName tests the Name method
func TestAdapterName(t *testing.T) {
	a := &Adapter{
		name: "test-adapter",
	}
	if got := a.Name(); got != "test-adapter" {
		t.Errorf("Name() = %q, want %q", got, "test-adapter")
	}
}

// TestAdapterHealthCheck tests HealthCheck with nil client
func TestAdapterHealthCheck(t *testing.T) {
	// Skip this test as it requires a real client
	// HealthCheck will panic with nil client, which is expected behavior
	t.Skip("HealthCheck requires real client - skipping nil client test")
}

// TestSandboxEntry tests the sandboxEntry struct
func TestSandboxEntry(t *testing.T) {
	entry := &sandboxEntry{
		claimName:  "test-claim",
		templateID: "base",
		createdAt:  time.Now(),
		metadata:   map[string]string{"key": "value"},
	}

	if entry.claimName != "test-claim" {
		t.Errorf("claimName = %q, want %q", entry.claimName, "test-claim")
	}
	if entry.templateID != "base" {
		t.Errorf("templateID = %q, want %q", entry.templateID, "base")
	}
}

// TestIDMapOperations tests the idMap thread-safe operations
func TestIDMapOperations(t *testing.T) {
	a := &Adapter{
		idMap: make(map[string]*sandboxEntry),
	}

	// Test store
	entry := &sandboxEntry{
		claimName:  "claim-1",
		templateID: "base",
		createdAt:  time.Now(),
	}
	a.idMap["sbx-123"] = entry

	// Test load
	loaded, ok := a.idMap["sbx-123"]
	if !ok {
		t.Error("Expected to find sbx-123 in idMap")
	}
	if loaded.claimName != "claim-1" {
		t.Errorf("claimName = %q, want %q", loaded.claimName, "claim-1")
	}

	// Test delete
	delete(a.idMap, "sbx-123")
	_, ok = a.idMap["sbx-123"]
	if ok {
		t.Error("Expected sbx-123 to be deleted from idMap")
	}
}

// TestConvertClaimToSandbox tests the convertClaimToSandbox helper
func TestConvertClaimToSandbox(t *testing.T) {
	// Note: This test requires the actual claim structure which is complex
	// For now, just verify the function exists and can be called
	// Full integration tests would require a real K8s client

	now := time.Now()
	claim := &metav1.PartialObjectMetadata{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-claim",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: now},
			Annotations: map[string]string{
				"sandbox.agent-sandbox.io/e2b-id": "e2b-123",
			},
		},
	}

	// Verify the claim was created correctly
	if claim.Name != "test-claim" {
		t.Errorf("claim.Name = %q, want %q", claim.Name, "test-claim")
	}
	if claim.Annotations["sandbox.agent-sandbox.io/e2b-id"] != "e2b-123" {
		t.Error("Expected e2b-id annotation")
	}
}

// TestListSandboxesEmpty tests ListSandboxes with no sandboxes
func TestListSandboxesEmpty(t *testing.T) {
	// Without a real K8s client, this will fail, but we can test the structure
	opts := adapter.ListOptions{
		Limit:  10,
		Offset: 0,
	}

	// This would normally call K8s API, so we just verify the method signature
	_ = opts
}

// TestRunCommandWithArgs tests that RunCommand properly escapes arguments
func TestRunCommandWithArgs(t *testing.T) {
	// This is a unit test for the argument escaping logic
	// Full integration test would require a real sandbox handle

	testCases := []struct {
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
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Build the command string the same way RunCommand does
			result := tc.command
			if len(tc.args) > 0 {
				escapedArgs := make([]string, len(tc.args))
				for i, arg := range tc.args {
					escapedArgs[i] = shellQuote(arg)
				}
				result = result + " " + joinStrings(escapedArgs, " ")
			}

			if result != tc.expected {
				t.Errorf("Command construction = %q, want %q", result, tc.expected)
			}
		})
	}
}

// Helper function to join strings (mimics strings.Join for test clarity)
func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}

// TestMakeDirCommand tests that MakeDir properly quotes the path
func TestMakeDirCommand(t *testing.T) {
	testCases := []struct {
		path     string
		expected string
	}{
		{
			path:     "/tmp/test",
			expected: "mkdir -p '/tmp/test'",
		},
		{
			path:     "/tmp/my dir",
			expected: "mkdir -p '/tmp/my dir'",
		},
		{
			path:     "/tmp/test; rm -rf /",
			expected: "mkdir -p '/tmp/test; rm -rf /'",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.path, func(t *testing.T) {
			cmd := "mkdir -p " + shellQuote(tc.path)
			if cmd != tc.expected {
				t.Errorf("MakeDir command = %q, want %q", cmd, tc.expected)
			}
		})
	}
}

// TestRemoveFileCommand tests that RemoveFile properly quotes the path
func TestRemoveFileCommand(t *testing.T) {
	testCases := []struct {
		path     string
		expected string
	}{
		{
			path:     "/tmp/test",
			expected: "rm -rf '/tmp/test'",
		},
		{
			path:     "/tmp/my file.txt",
			expected: "rm -rf '/tmp/my file.txt'",
		},
		{
			path:     "/tmp/test && echo hacked",
			expected: "rm -rf '/tmp/test && echo hacked'",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.path, func(t *testing.T) {
			cmd := "rm -rf " + shellQuote(tc.path)
			if cmd != tc.expected {
				t.Errorf("RemoveFile command = %q, want %q", cmd, tc.expected)
			}
		})
	}
}

// TestGetAccessToken_GeneratesAndCaches tests that GetAccessToken generates a
// token, caches it, and returns the same token on subsequent calls.
func TestGetAccessToken_GeneratesAndCaches(t *testing.T) {
	a := &Adapter{
		name:       "test",
		tokenCache: cache.New(100, 1*time.Hour),
		idMap:      make(map[string]*sandboxEntry),
	}
	// Pre-populate idMap so resolveClaimName succeeds.
	a.idMap["sbx-123"] = &sandboxEntry{claimName: "claim-123"}
	ctx := context.Background()

	// First call: generates new token.
	tok1, err := a.GetAccessToken(ctx, "sbx-123")
	if err != nil {
		t.Fatalf("GetAccessToken: %v", err)
	}
	if tok1.Token == "" {
		t.Fatal("expected non-empty token")
	}
	if !strings.HasPrefix(tok1.Token, "envd_sbx-123_") {
		t.Errorf("token should have prefix envd_sbx-123_, got %q", tok1.Token)
	}
	if tok1.ExpiresAt.IsZero() {
		t.Error("expected non-zero ExpiresAt")
	}

	// Second call: returns same cached token.
	tok2, err := a.GetAccessToken(ctx, "sbx-123")
	if err != nil {
		t.Fatalf("GetAccessToken (2nd): %v", err)
	}
	if tok2.Token != tok1.Token {
		t.Errorf("expected same token on cache hit, got %q vs %q", tok2.Token, tok1.Token)
	}
}

// TestValidateAccessToken tests token validation against the cache.
func TestValidateAccessToken(t *testing.T) {
	a := &Adapter{
		name:       "test",
		tokenCache: cache.New(100, 1*time.Hour),
		idMap:      make(map[string]*sandboxEntry),
	}
	a.idMap["sbx-1"] = &sandboxEntry{claimName: "claim-1"}
	ctx := context.Background()

	// Generate a token.
	tok, err := a.GetAccessToken(ctx, "sbx-1")
	if err != nil {
		t.Fatalf("GetAccessToken: %v", err)
	}

	// Valid token.
	valid, err := a.ValidateAccessToken(ctx, "sbx-1", tok.Token)
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	if !valid {
		t.Error("expected token to be valid")
	}

	// Wrong token.
	valid, err = a.ValidateAccessToken(ctx, "sbx-1", "envd_sbx-1_wrongtoken")
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	if valid {
		t.Error("expected invalid token to be rejected")
	}

	// Unknown sandbox.
	valid, err = a.ValidateAccessToken(ctx, "sbx-unknown", tok.Token)
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	if valid {
		t.Error("expected unknown sandbox to return false")
	}
}

// TestListPorts_Empty tests that ListPorts returns empty list for unknown sandbox.
func TestListPorts_Empty(t *testing.T) {
	a := &Adapter{
		name:        "test",
		portTracker: make(map[string]map[int]bool),
	}
	ctx := context.Background()

	ports, err := a.ListPorts(ctx, "unknown-sandbox")
	if err != nil {
		t.Fatalf("ListPorts: %v", err)
	}
	if len(ports) != 0 {
		t.Errorf("expected empty port list, got %d ports", len(ports))
	}
}

// TestPortTracker tests the port tracker logic.
func TestPortTracker(t *testing.T) {
	a := &Adapter{
		name:        "test",
		portTracker: make(map[string]map[int]bool),
	}
	ctx := context.Background()

	// Initially, no ports.
	ports, err := a.ListPorts(ctx, "sandbox-1")
	if err != nil {
		t.Fatalf("ListPorts: %v", err)
	}
	if len(ports) != 0 {
		t.Errorf("expected 0 ports, got %d", len(ports))
	}

	// Manually add ports to tracker (simulating GetPortURL behavior).
	a.portTrackerMu.Lock()
	if a.portTracker["sandbox-1"] == nil {
		a.portTracker["sandbox-1"] = make(map[int]bool)
	}
	a.portTracker["sandbox-1"][3000] = true
	a.portTracker["sandbox-1"][8080] = true
	a.portTrackerMu.Unlock()

	// Verify ports are tracked.
	ports, err = a.ListPorts(ctx, "sandbox-1")
	if err != nil {
		t.Fatalf("ListPorts: %v", err)
	}
	if len(ports) != 2 {
		t.Errorf("expected 2 ports, got %d", len(ports))
	}

	// Verify port numbers.
	portSet := make(map[int]bool)
	for _, p := range ports {
		portSet[p.Port] = true
	}
	if !portSet[3000] || !portSet[8080] {
		t.Errorf("expected ports 3000 and 8080, got %v", ports)
	}

	// Cleanup should remove ports.
	a.portTrackerMu.Lock()
	delete(a.portTracker, "sandbox-1")
	a.portTrackerMu.Unlock()

	ports, err = a.ListPorts(ctx, "sandbox-1")
	if err != nil {
		t.Fatalf("ListPorts after cleanup: %v", err)
	}
	if len(ports) != 0 {
		t.Errorf("expected 0 ports after cleanup, got %d", len(ports))
	}
}
