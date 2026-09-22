package envd

import (
	"context"
	"fmt"
)

// FilesystemService is the name of the filesystem.Filesystem ConnectRPC service.
const FilesystemService = "filesystem.Filesystem"

// StatRequest is the request for filesystem.Filesystem/Stat.
type StatRequest struct {
	Path string `json:"path"`
}

// StatResponse is the response for filesystem.Filesystem/Stat.
type StatResponse struct {
	Entry *FileInfo `json:"entry"`
}

// ListDirRequest is the request for filesystem.Filesystem/ListDir.
type ListDirRequest struct {
	Path  string `json:"path"`
	Depth int32  `json:"depth,omitempty"`
}

// ListDirResponse is the response for filesystem.Filesystem/ListDir.
type ListDirResponse struct {
	Entries []*FileInfo `json:"entries"`
}

// FileInfo contains information about a file or directory.
// Field types match the protobuf EntryInfo spec JSON mapping (see
// packages/envd/spec/filesystem/filesystem.proto in e2b-dev/infra).
//
// Proto3 JSON mapping notes:
//   - int64/uint64 fields are encoded as JSON strings (to avoid JS precision loss)
//   - uint32/int32 fields are encoded as JSON numbers
type FileInfo struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	Type          string `json:"type"` // "FILE_TYPE_FILE", "FILE_TYPE_DIRECTORY", etc.
	Size          string `json:"size"` // int64 proto → JSON string
	Mode          uint32 `json:"mode,omitempty"`
	Permissions   string `json:"permissions,omitempty"`
	Owner         string `json:"owner,omitempty"`
	Group         string `json:"group,omitempty"`
	ModifiedTime  string `json:"modifiedTime,omitempty"` // RFC 3339
	SymlinkTarget string `json:"symlinkTarget,omitempty"`
}

// SizeInt64 returns the Size as int64. Returns 0 if the string is not a valid number.
func (fi *FileInfo) SizeInt64() int64 {
	if fi == nil || fi.Size == "" {
		return 0
	}
	var n int64
	if _, err := fmt.Sscanf(fi.Size, "%d", &n); err == nil {
		return n
	}
	return 0
}

// MakeDirRequest is the request for filesystem.Filesystem/MakeDir.
type MakeDirRequest struct {
	Path string `json:"path"`
}

// RemoveRequest is the request for filesystem.Filesystem/Remove.
type RemoveRequest struct {
	Path string `json:"path"`
}

// MoveRequest is the request for filesystem.Filesystem/Move.
type MoveRequest struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// Stat returns file metadata.
func (c *Client) Stat(ctx context.Context, path string) (*FileInfo, error) {
	req := &StatRequest{Path: path}
	var resp StatResponse

	if err := c.doConnectRPC(ctx, FilesystemService, "Stat", req, &resp); err != nil {
		return nil, fmt.Errorf("stat %q: %w", path, err)
	}

	return resp.Entry, nil
}

// ListDir lists directory contents.
func (c *Client) ListDir(ctx context.Context, path string, depth int32) ([]*FileInfo, error) {
	req := &ListDirRequest{Path: path, Depth: depth}
	var resp ListDirResponse

	if err := c.doConnectRPC(ctx, FilesystemService, "ListDir", req, &resp); err != nil {
		return nil, fmt.Errorf("list dir %q: %w", path, err)
	}

	return resp.Entries, nil
}

// MakeDir creates a directory.
func (c *Client) MakeDir(ctx context.Context, path string) error {
	req := &MakeDirRequest{Path: path}

	if err := c.doConnectRPC(ctx, FilesystemService, "MakeDir", req, nil); err != nil {
		return fmt.Errorf("make dir %q: %w", path, err)
	}

	return nil
}

// Remove removes a file or directory.
func (c *Client) Remove(ctx context.Context, path string) error {
	req := &RemoveRequest{Path: path}

	if err := c.doConnectRPC(ctx, FilesystemService, "Remove", req, nil); err != nil {
		return fmt.Errorf("remove %q: %w", path, err)
	}

	return nil
}

// Move moves or renames a file.
func (c *Client) Move(ctx context.Context, src, dst string) error {
	req := &MoveRequest{Source: src, Destination: dst}

	if err := c.doConnectRPC(ctx, FilesystemService, "Move", req, nil); err != nil {
		return fmt.Errorf("move %q to %q: %w", src, dst, err)
	}

	return nil
}

// IsDir returns true if the file info represents a directory.
func (fi *FileInfo) IsDir() bool {
	return fi.Type == "FILE_TYPE_DIRECTORY"
}
