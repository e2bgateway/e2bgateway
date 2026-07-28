// Package dto defines the E2B wire format types for protocol compatibility.
// These types match the actual E2B API request/response schemas.
package dto

import "time"

// --- Sandbox ---

// SandboxCreateRequest is the E2B request body for creating a sandbox.
type SandboxCreateRequest struct {
	TemplateID   string            `json:"templateID"`
	Alias        string            `json:"alias,omitempty"`
	Timeout      int               `json:"timeout,omitempty"`    // seconds, default 300, max 3600
	MemoryMB     int               `json:"memoryMB,omitempty"`
	CPUCount     int               `json:"cpuCount,omitempty"`
	DiskSizeMB   int               `json:"diskSizeMB,omitempty"`
	StartCmd     string            `json:"startCmd,omitempty"`
	EnvVars      map[string]string `json:"envVars,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	Secure       bool              `json:"secure,omitempty"`
	VolumeMounts []VolumeMount     `json:"volumeMounts,omitempty"`
}

// SandboxCreateResponse is the E2B response for sandbox creation.
// The E2B SDK reads sandboxDomain to construct the envd ConnectRPC URL:
//
//	https://{port}-{sandboxID}.{sandboxDomain}
//
// envdAccessToken authenticates SDK→envd requests; trafficAccessToken is
// an optional additional token the SDK forwards on every envd call.
type SandboxCreateResponse struct {
	SandboxID          string `json:"sandboxID"`
	TemplateID         string `json:"templateID"`
	Alias              string `json:"alias,omitempty"`
	ClientID           string `json:"clientID"`
	EnvdVersion        string `json:"envdVersion"`
	EnvdAccessToken    string `json:"envdAccessToken,omitempty"`
	SandboxDomain      string `json:"sandboxDomain,omitempty"`
	TrafficAccessToken string `json:"trafficAccessToken,omitempty"`
}

// SandboxInfo is the E2B response for sandbox details.
type SandboxInfo struct {
	SandboxID    string            `json:"sandboxID"`
	TemplateID   string            `json:"templateID"`
	Alias        string            `json:"alias,omitempty"`
	ClientID     string            `json:"clientID"`
	StartedAt    time.Time         `json:"startedAt"`
	EndAt        time.Time         `json:"endAt"`
	CPUCount     int               `json:"cpuCount"`
	MemoryMB     int               `json:"memoryMB"`
	DiskSizeMB   int               `json:"diskSizeMB"`
	State        string            `json:"state"`
	EnvdVersion  string            `json:"envdVersion,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	VolumeMounts []VolumeMount     `json:"volumeMounts,omitempty"`
}

// VolumeMount represents a volume mount in a sandbox.
type VolumeMount struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// SandboxResumeRequest is the request body for resuming a sandbox.
type SandboxResumeRequest struct {
	Timeout int `json:"timeout,omitempty"`
}

// SandboxTimeoutRequest is the request body for setting sandbox timeout.
type SandboxTimeoutRequest struct {
	Timeout int `json:"timeout"` // seconds from now
}

// --- Code Execution ---

// CodeExecRequest is the E2B request body for code execution.
type CodeExecRequest struct {
	Code     string            `json:"code"`
	Language string            `json:"language,omitempty"` // python, javascript
	EnvVars  map[string]string `json:"envVars,omitempty"`
}

// CodeExecResult is the E2B response for code execution.
type CodeExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
	Error    string `json:"error,omitempty"`
}

// --- Commands ---

// CommandRequest is the E2B request body for running a command.
type CommandRequest struct {
	Command string            `json:"command"`
	Cwd     string            `json:"cwd,omitempty"`
	EnvVars map[string]string `json:"envVars,omitempty"`
	Timeout int               `json:"timeout,omitempty"` // seconds
}

// CommandResult is the E2B response for command execution.
type CommandResult struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exitCode"`
	ProcessID string `json:"processID,omitempty"`
}

// --- Filesystem ---

// FileWriteRequest is the E2B request body for writing a file.
type FileWriteRequest struct {
	Path        string `json:"path"`
	Content     string `json:"content"`
	Permissions string `json:"permissions,omitempty"`
}

// FileListRequest is the E2B request body for listing a directory.
type FileListRequest struct {
	Path string `json:"path"`
}

// FileListResponse is the E2B response for directory listing.
type FileListResponse struct {
	Entries []FileEntry `json:"entries"`
}

// FileEntry represents a file or directory in a listing.
type FileEntry struct {
	Name         string    `json:"name"`
	Type         string    `json:"type"` // "file" or "dir"
	Size         int64     `json:"size"`
	LastModified time.Time `json:"lastModified,omitempty"`
}

// FileRemoveRequest is the E2B request body for removing a file.
type FileRemoveRequest struct {
	Path string `json:"path"`
}

// MakeDirRequest is the E2B request body for creating a directory.
type MakeDirRequest struct {
	Path string `json:"path"`
}

// --- Processes ---

// ProcessInfo represents a running process in the sandbox.
type ProcessInfo struct {
	ProcessID string    `json:"processID"`
	Command   string    `json:"command"`
	PID       int       `json:"pid"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"startedAt"`
}

// SendStdinRequest is the request body for sending stdin to a process.
type SendStdinRequest struct {
	Data string `json:"data"`
}

// --- Ports ---

// PortInfo represents an open port in the sandbox.
type PortInfo struct {
	Port  int  `json:"port"`
	Ready bool `json:"ready"`
}

// PortsResponse is the response for listing open ports.
type PortsResponse struct {
	Ports []PortInfo `json:"ports"`
}

// PortURLResponse is the response for getting a port's public URL.
type PortURLResponse struct {
	URL string `json:"url"`
}

// --- Snapshots ---

// SnapshotCreateRequest is the request body for creating a snapshot.
type SnapshotCreateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SnapshotInfo represents a snapshot.
type SnapshotInfo struct {
	SnapshotID  string    `json:"snapshotID"`
	SandboxID   string    `json:"sandboxID"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	SizeMB      int       `json:"sizeMB,omitempty"`
}

// --- Access Token ---

// AccessTokenResponse is the response for getting an access token.
type AccessTokenResponse struct {
	AccessToken string    `json:"accessToken"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

// --- Templates ---

// TemplateInfo represents a template.
type TemplateInfo struct {
	TemplateID string    `json:"templateID"`
	Aliases    []string  `json:"aliases,omitempty"`
	BuildID    string    `json:"buildID,omitempty"`
	CPUCount   int       `json:"cpuCount,omitempty"`
	MemoryMB   int       `json:"memoryMB,omitempty"`
	Public     bool      `json:"public"`
	Ready      bool      `json:"ready"`
	CreatedAt  time.Time `json:"createdAt"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
}

// TemplateBuildRequest is the request body for building a template.
type TemplateBuildRequest struct {
	Name       string      `json:"name,omitempty"`
	Dockerfile string      `json:"dockerfile,omitempty"`
	StartCmd   string      `json:"startCmd,omitempty"`
	CPUCount   int         `json:"cpuCount,omitempty"`
	MemoryMB   int         `json:"memoryMB,omitempty"`
	Steps      []BuildStep `json:"steps,omitempty"`
}

// BuildStep represents a step in a template build.
type BuildStep struct {
	Type    string `json:"type"`              // "run", "write", "copy"
	Command string `json:"command,omitempty"` // for "run"
	Path    string `json:"path,omitempty"`    // for "write"/"copy"
	Content string `json:"content,omitempty"` // for "write"
}

// TemplateBuildResponse is the response for starting a template build.
type TemplateBuildResponse struct {
	TemplateID string `json:"templateID"`
	BuildID    string `json:"buildID"`
	Status     string `json:"status"`
}

// BuildStatusResponse is the response for getting build status.
type BuildStatusResponse struct {
	BuildID string `json:"buildID"`
	Status  string `json:"status"` // "building", "ready", "error"
	Logs    string `json:"logs,omitempty"`
	Error   string `json:"error,omitempty"`
}

// AliasRequest is the request body for creating a template alias.
type AliasRequest struct {
	Alias string `json:"alias"`
}

// --- Warm Pools ---

// WarmPoolInfo represents a warm pool.
type WarmPoolInfo struct {
	WarmPoolID  string    `json:"warmPoolID"`
	TemplateID  string    `json:"templateID"`
	TargetSize  int       `json:"targetSize"`
	CurrentSize int       `json:"currentSize"`
	Status      string    `json:"status,omitempty"`
	CreatedAt   time.Time `json:"createdAt,omitempty"`
}

// WarmPoolCreateRequest is the request body for creating a warm pool.
type WarmPoolCreateRequest struct {
	TemplateID string `json:"templateID"`
	TargetSize int    `json:"targetSize"`
	WarmPoolID string `json:"warmPoolID,omitempty"`
}

// WarmPoolSizeRequest is the request body for updating warm pool size.
type WarmPoolSizeRequest struct {
	TargetSize int `json:"targetSize"`
}

// --- Error ---

// ErrorResponse is the standard E2B error response.
type ErrorResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// --- WebSocket ---

// WSMessage is the generic WebSocket message frame.
type WSMessage struct {
	Type string      `json:"type"`
	ID   string      `json:"id,omitempty"`
	Data interface{} `json:"data,omitempty"`
}

// WSCommandMessage is a client→server command execution request.
type WSCommandMessage struct {
	Type    string            `json:"type"` // "command"
	ID      string            `json:"id"`
	Command string            `json:"command"`
	Cwd     string            `json:"cwd,omitempty"`
	EnvVars map[string]string `json:"envVars,omitempty"`
	PTY     bool              `json:"pty,omitempty"`
}

// WSTerminalStartMessage starts a terminal session.
type WSTerminalStartMessage struct {
	Type string `json:"type"` // "terminal:start"
	ID   string `json:"id"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// WSTerminalInputMessage sends input to a terminal.
type WSTerminalInputMessage struct {
	Type string `json:"type"` // "terminal:input"
	ID   string `json:"id"`
	Data string `json:"data"`
}

// WSTerminalResizeMessage resizes a terminal.
type WSTerminalResizeMessage struct {
	Type string `json:"type"` // "terminal:resize"
	ID   string `json:"id"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// WSOutputMessage is a server→client output message.
type WSOutputMessage struct {
	Type string `json:"type"` // "stdout", "stderr"
	ID   string `json:"id"`
	Data string `json:"data"`
}

// WSExitMessage is a server→client process exit message.
type WSExitMessage struct {
	Type     string `json:"type"` // "exit"
	ID       string `json:"id"`
	ExitCode int    `json:"exitCode"`
}

// WSErrorMessage is a server→client error message.
type WSErrorMessage struct {
	Type  string        `json:"type"` // "error"
	ID    string        `json:"id"`
	Error WSErrorDetail `json:"error"`
}

// WSErrorDetail contains error details in a WebSocket error message.
type WSErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// --- Environment Variables ---

// SetEnvsRequest is the request body for setting environment variables.
type SetEnvsRequest struct {
	Envs map[string]string `json:"envs"`
}

// --- Logs ---

// LogEntry represents a single log entry from a sandbox (DTO wire format).
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
	Level     string    `json:"level,omitempty"`
	Source    string    `json:"source,omitempty"`
}

// SandboxLogsResponse is the response for getting sandbox logs.
type SandboxLogsResponse struct {
	Logs []LogEntry `json:"logs"`
}

// --- Metrics ---

// SandboxMetrics represents sandbox resource metrics.
type SandboxMetrics struct {
	CPUUsage    float64 `json:"cpuUsage"`    // percentage 0-100
	MemoryUsage int64   `json:"memoryUsage"` // bytes
	DiskUsage   int64   `json:"diskUsage"`   // bytes
	NetworkRx   int64   `json:"networkRx"`   // bytes received
	NetworkTx   int64   `json:"networkTx"`   // bytes transmitted
	Timestamp   time.Time `json:"timestamp"`
}

// --- File Move ---

// MoveFileRequest is the request body for moving a file.
type MoveFileRequest struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// --- Template Tags ---

// TagInfo represents a template tag.
type TagInfo struct {
	Name       string    `json:"name"`
	TemplateID string    `json:"templateID"`
	BuildID    string    `json:"buildID"`
	CreatedAt  time.Time `json:"createdAt"`
}

// CreateTagRequest is the request body for creating a tag.
type CreateTagRequest struct {
	Name    string `json:"name"`
	BuildID string `json:"buildID,omitempty"`
}

// --- Update Template (v2) ---

// UpdateTemplateRequest is the request body for updating a template (v2).
type UpdateTemplateRequest struct {
	Name     string `json:"name,omitempty"`
	Public   *bool  `json:"public,omitempty"`
	CPUCount *int   `json:"cpuCount,omitempty"`
	MemoryMB *int   `json:"memoryMB,omitempty"`
}

// --- v2 Sandbox List ---

// V2SandboxListResponse is the v2 response for listing sandboxes.
type V2SandboxListResponse struct {
	Sandboxes []SandboxInfo `json:"sandboxes"`
	NextToken string        `json:"nextToken,omitempty"`
}

// --- v2 Template Types ---

// V2TemplateInfo is the v2 template response.
type V2TemplateInfo struct {
	TemplateID string    `json:"templateID"`
	BuildID    string    `json:"buildID"`
	Aliases    []string  `json:"aliases,omitempty"`
	Tags       []TagInfo `json:"tags,omitempty"`
	CPUCount   int       `json:"cpuCount"`
	MemoryMB   int       `json:"memoryMB"`
	Public     bool      `json:"public"`
	Ready      bool      `json:"ready"`
	CreatedAt  time.Time `json:"createdAt"`
}

// V2TemplateCreateRequest is the v2 request body for creating a template.
type V2TemplateCreateRequest struct {
	Name       string      `json:"name"`
	Dockerfile string      `json:"dockerfile,omitempty"`
	StartCmd   string      `json:"startCmd,omitempty"`
	CPUCount   int         `json:"cpuCount,omitempty"`
	MemoryMB   int         `json:"memoryMB,omitempty"`
	Steps      []BuildStep `json:"steps,omitempty"`
	Tags       []string    `json:"tags,omitempty"`
}
