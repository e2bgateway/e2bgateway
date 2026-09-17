package envd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/filesystem.Filesystem/Stat" {
			t.Errorf("expected path /filesystem.Filesystem/Stat, got %q", r.URL.Path)
			return
		}

		var req StatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req.Path != "/tmp/test.txt" {
			t.Errorf("expected path /tmp/test.txt, got %q", req.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		resp := StatResponse{
			Entry: &FileInfo{
				Name: "test.txt",
				Path: "/tmp/test.txt",
				Type: "FILE_TYPE_FILE",
				Size: "1024",
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
	})

	info, err := client.Stat(context.Background(), "/tmp/test.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.Name != "test.txt" {
		t.Errorf("expected name test.txt, got %q", info.Name)
	}

	if info.Path != "/tmp/test.txt" {
		t.Errorf("expected path /tmp/test.txt, got %q", info.Path)
	}

	if info.Type != "FILE_TYPE_FILE" {
		t.Errorf("expected type FILE_TYPE_FILE, got %q", info.Type)
	}

	if info.Size != "1024" {
		t.Errorf("expected size 1024, got %q", info.Size)
	}

	if info.IsDir() {
		t.Error("expected IsDir() to return false")
	}
}

func TestListDir(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/filesystem.Filesystem/ListDir" {
			t.Errorf("expected path /filesystem.Filesystem/ListDir, got %q", r.URL.Path)
			return
		}

		var req ListDirRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req.Path != "/tmp" {
			t.Errorf("expected path /tmp, got %q", req.Path)
		}

		if req.Depth != 1 {
			t.Errorf("expected depth 1, got %d", req.Depth)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		resp := ListDirResponse{
			Entries: []*FileInfo{
				{Name: "file1.txt", Path: "/tmp/file1.txt", Type: "FILE_TYPE_FILE", Size: "100"},
				{Name: "dir1", Path: "/tmp/dir1", Type: "FILE_TYPE_DIRECTORY", Size: "0"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
	})

	entries, err := client.ListDir(context.Background(), "/tmp", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	if entries[0].Name != "file1.txt" || entries[0].IsDir() {
		t.Errorf("expected file1.txt (not dir), got %s (isDir=%v)", entries[0].Name, entries[0].IsDir())
	}

	if entries[1].Name != "dir1" || !entries[1].IsDir() {
		t.Errorf("expected dir1 (is dir), got %s (isDir=%v)", entries[1].Name, entries[1].IsDir())
	}
}

func TestMakeDir(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/filesystem.Filesystem/MakeDir" {
			t.Errorf("expected path /filesystem.Filesystem/MakeDir, got %q", r.URL.Path)
			return
		}

		var req MakeDirRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req.Path != "/tmp/newdir" {
			t.Errorf("expected path /tmp/newdir, got %q", req.Path)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
	})

	err := client.MakeDir(context.Background(), "/tmp/newdir")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRemove(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/filesystem.Filesystem/Remove" {
			t.Errorf("expected path /filesystem.Filesystem/Remove, got %q", r.URL.Path)
			return
		}

		var req RemoveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req.Path != "/tmp/test.txt" {
			t.Errorf("expected path /tmp/test.txt, got %q", req.Path)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
	})

	err := client.Remove(context.Background(), "/tmp/test.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMove(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/filesystem.Filesystem/Move" {
			t.Errorf("expected path /filesystem.Filesystem/Move, got %q", r.URL.Path)
			return
		}

		var req MoveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req.Source != "/tmp/old.txt" {
			t.Errorf("expected source /tmp/old.txt, got %q", req.Source)
		}

		if req.Destination != "/tmp/new.txt" {
			t.Errorf("expected destination /tmp/new.txt, got %q", req.Destination)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
	})

	err := client.Move(context.Background(), "/tmp/old.txt", "/tmp/new.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFileInfo_IsDir(t *testing.T) {
	tests := []struct {
		name     string
		fileType string
		expected bool
	}{
		{"file", "FILE_TYPE_FILE", false},
		{"directory", "FILE_TYPE_DIRECTORY", true},
		{"unknown", "FILE_TYPE_UNKNOWN", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fi := &FileInfo{Type: tt.fileType}
			if fi.IsDir() != tt.expected {
				t.Errorf("expected IsDir()=%v for type %q, got %v", tt.expected, tt.fileType, fi.IsDir())
			}
		})
	}
}
