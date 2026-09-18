package envd

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUploadFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/files" {
			t.Errorf("expected path /files, got %q", r.URL.Path)
			return
		}

		if r.Method != http.MethodPost {
			t.Errorf("expected method POST, got %q", r.Method)
			return
		}

		// Verify path is in query parameter (envd API spec)
		path := r.URL.Query().Get("path")
		if path != "/tmp/test.txt" {
			t.Errorf("expected path query parameter /tmp/test.txt, got %q", path)
		}

		// Verify headers
		if r.Header.Get("E2b-Sandbox-Id") != "test-sandbox" {
			t.Errorf("expected E2b-Sandbox-Id test-sandbox, got %q", r.Header.Get("E2b-Sandbox-Id"))
		}
		if r.Header.Get("X-Access-Token") != "test-token" {
			t.Errorf("expected X-Access-Token test-token, got %q", r.Header.Get("X-Access-Token"))
		}

		// Parse multipart form (should have only file part now)
		contentType := r.Header.Get("Content-Type")
		mediaType, params, err := mime.ParseMediaType(contentType)
		if err != nil {
			t.Fatalf("failed to parse content type: %v", err)
		}

		if !strings.HasPrefix(mediaType, "multipart/") {
			t.Fatalf("expected multipart content type, got %q", mediaType)
		}

		reader := multipart.NewReader(r.Body, params["boundary"])

		// Read file part (first and only part)
		filePart, err := reader.NextPart()
		if err != nil {
			t.Fatalf("failed to read file part: %v", err)
		}
		if filePart.FormName() != "file" {
			t.Errorf("expected file part, got %q", filePart.FormName())
		}
		fileBytes, _ := io.ReadAll(filePart)
		if string(fileBytes) != "test content" {
			t.Errorf("expected file content 'test content', got %q", string(fileBytes))
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
	})

	err := client.UploadFile(context.Background(), "/tmp/test.txt", strings.NewReader("test content"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDownloadFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/files" {
			t.Errorf("expected path /files, got %q", r.URL.Path)
			return
		}

		if r.Method != http.MethodGet {
			t.Errorf("expected method GET, got %q", r.Method)
			return
		}

		// Verify query parameter
		path := r.URL.Query().Get("path")
		if path != "/tmp/test.txt" {
			t.Errorf("expected path query parameter /tmp/test.txt, got %q", path)
		}

		// Verify headers
		if r.Header.Get("E2b-Sandbox-Id") != "test-sandbox" {
			t.Errorf("expected E2b-Sandbox-Id test-sandbox, got %q", r.Header.Get("E2b-Sandbox-Id"))
		}
		if r.Header.Get("X-Access-Token") != "test-token" {
			t.Errorf("expected X-Access-Token test-token, got %q", r.Header.Get("X-Access-Token"))
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("file content"))
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
	})

	reader, err := client.DownloadFile(context.Background(), "/tmp/test.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer reader.Close()

	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read content: %v", err)
	}

	if string(content) != "file content" {
		t.Errorf("expected content 'file content', got %q", string(content))
	}
}

func TestDownloadFile_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("file not found"))
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
	})

	_, err := client.DownloadFile(context.Background(), "/tmp/nonexistent.txt")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !containsString(err.Error(), "404") {
		t.Errorf("expected error to contain '404', got %q", err.Error())
	}
}

func TestUploadFileWithOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify path in query param
		path := r.URL.Query().Get("path")
		if path != "/tmp/test.txt" {
			t.Errorf("expected path query /tmp/test.txt, got %q", path)
		}

		// Verify username header
		if r.Header.Get("X-Username") != "testuser" {
			t.Errorf("expected X-Username testuser, got %q", r.Header.Get("X-Username"))
		}

		// Verify custom metadata header
		if r.Header.Get("X-Metadata-Key") != "value" {
			t.Errorf("expected X-Metadata-Key value, got %q", r.Header.Get("X-Metadata-Key"))
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
	})

	opts := UploadFileOptions{
		Path:     "/tmp/test.txt",
		Reader:   strings.NewReader("test content"),
		Username: "testuser",
		Metadata: map[string]string{"key": "value"},
	}

	err := client.UploadFileWithOptions(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDownloadFileToWriter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("test data"))
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
	})

	var buf bytes.Buffer
	err := client.DownloadFileToWriter(context.Background(), "/tmp/test.txt", &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if buf.String() != "test data" {
		t.Errorf("expected 'test data', got %q", buf.String())
	}
}

func TestUploadFile_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
	})

	err := client.UploadFile(context.Background(), "/tmp/test.txt", strings.NewReader("content"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !containsString(err.Error(), "500") {
		t.Errorf("expected error to contain '500', got %q", err.Error())
	}
}
