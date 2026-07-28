// Package api provides public E2B API types.
package api

import "time"

// Sandbox represents a sandbox instance in the E2B API.
type Sandbox struct {
	SandboxID  string            `json:"sandboxID"`
	TemplateID string            `json:"templateID"`
	Alias      string            `json:"alias,omitempty"`
	StartedAt  time.Time         `json:"startedAt"`
	EndAt      time.Time         `json:"endAt"`
	Status     string            `json:"status"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	ClientID   string            `json:"clientID,omitempty"`
}

// SandboxCreateRequest is the request body for creating a sandbox.
type SandboxCreateRequest struct {
	TemplateID string            `json:"templateID"`
	Timeout    int               `json:"timeout,omitempty"` // seconds
	Alias      string            `json:"alias,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// CodeExecutionRequest is the request body for code execution.
type CodeExecutionRequest struct {
	Code     string            `json:"code"`
	Language string            `json:"language,omitempty"`
	EnvVars  map[string]string `json:"envVars,omitempty"`
}

// CodeExecutionResult is the response body for code execution.
type CodeExecutionResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
	Error    string `json:"error,omitempty"`
}

// CommandRequest is the request body for running a command.
type CommandRequest struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`
}

// CommandResult is the response body for command execution.
type CommandResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

// Template represents a sandbox template.
type Template struct {
	TemplateID  string    `json:"templateID"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CPUCount    int       `json:"cpuCount,omitempty"`
	MemoryMB    int       `json:"memoryMB,omitempty"`
	Public      bool      `json:"public,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// ErrorResponse is the standard E2B error response format.
type ErrorResponse struct {
	Code    int    `json:"code"`
	Detail  string `json:"detail"`
	Message string `json:"message"`
}
