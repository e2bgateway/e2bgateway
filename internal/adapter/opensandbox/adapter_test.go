package opensandbox

import (
	"testing"

	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
)

func TestAdapter_Name(t *testing.T) {
	a := &Adapter{name: "test-opensandbox"}
	if got := a.Name(); got != "test-opensandbox" {
		t.Errorf("Name() = %q, want %q", got, "test-opensandbox")
	}
}

func TestMapState(t *testing.T) {
	tests := []struct {
		name     string
		state    opensandbox.SandboxState
		expected string
	}{
		{"running", opensandbox.StateRunning, "running"},
		{"paused", opensandbox.StatePaused, "paused"},
		{"terminated", opensandbox.StateTerminated, "stopped"},
		{"pending", opensandbox.StatePending, "starting"},
		{"unknown", "unknown", "starting"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapState(tt.state)
			if string(got) != tt.expected {
				t.Errorf("mapState(%q) = %q, want %q", tt.state, got, tt.expected)
			}
		})
	}
}

func TestWrapCodeInCommand(t *testing.T) {
	tests := []struct {
		code     string
		language string
		want     string
	}{
		{"print('hello')", "python", "python3 -c \"print('hello')\""},
		{"print('hello')", "python3", "python3 -c \"print('hello')\""},
		{"console.log('hi')", "javascript", "node -e \"console.log('hi')\""},
		{"console.log('hi')", "node", "node -e \"console.log('hi')\""},
		{"echo hello", "bash", "echo hello"},
		{"echo hello", "sh", "echo hello"},
		{"some code", "", "python3 -c \"some code\""}, // default to python
	}

	for _, tt := range tests {
		got := wrapCodeInCommand(tt.code, tt.language)
		if got != tt.want {
			t.Errorf("wrapCodeInCommand(%q, %q) = %q, want %q", tt.code, tt.language, got, tt.want)
		}
	}
}
