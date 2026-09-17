package envd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	cfg := ClientConfig{
		BaseURL:     "http://localhost:49983",
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
		Timeout:     10 * time.Second,
	}

	client := NewClient(cfg)

	if client == nil {
		t.Fatal("expected non-nil client")
	}

	if client.BaseURL() != cfg.BaseURL {
		t.Errorf("expected BaseURL %q, got %q", cfg.BaseURL, client.BaseURL())
	}

	if client.SandboxID() != cfg.SandboxID {
		t.Errorf("expected SandboxID %q, got %q", cfg.SandboxID, client.SandboxID())
	}
}

func TestNewClient_DefaultTimeout(t *testing.T) {
	cfg := ClientConfig{
		BaseURL:     "http://localhost:49983",
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
	}

	client := NewClient(cfg)

	if client.httpClient.Timeout != 30*time.Second {
		t.Errorf("expected default timeout 30s, got %v", client.httpClient.Timeout)
	}
}

func TestDoConnectRPC_Success(t *testing.T) {
	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers (ConnectRPC uses application/connect+json)
		if r.Header.Get("Content-Type") != "application/connect+json" {
			t.Errorf("expected Content-Type application/connect+json, got %q", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("Connect-Protocol-Version") != "1" {
			t.Errorf("expected Connect-Protocol-Version 1, got %q", r.Header.Get("Connect-Protocol-Version"))
		}
		if r.Header.Get("E2b-Sandbox-Id") != "test-sandbox" {
			t.Errorf("expected E2b-Sandbox-Id test-sandbox, got %q", r.Header.Get("E2b-Sandbox-Id"))
		}
		if r.Header.Get("X-Access-Token") != "test-token" {
			t.Errorf("expected X-Access-Token test-token, got %q", r.Header.Get("X-Access-Token"))
		}

		// Verify request path
		if r.URL.Path != "/test.Service/TestMethod" {
			t.Errorf("expected path /test.Service/TestMethod, got %q", r.URL.Path)
		}

		// Decode envelope-framed request
		var req map[string]string
		if err := readEnvelopeRequest(r, &req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if req["key"] != "value" {
			t.Errorf("expected request key=value, got %q", req["key"])
		}

		// Send envelope-framed response
		writeEnvelopeResponse(w, map[string]string{"result": "success"})
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
	})

	var resp map[string]string
	err := client.doConnectRPC(context.Background(), "test.Service", "TestMethod",
		map[string]string{"key": "value"}, &resp)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp["result"] != "success" {
		t.Errorf("expected result=success, got %q", resp["result"])
	}
}

func TestDoConnectRPC_ErrorStatus(t *testing.T) {
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

	err := client.doConnectRPC(context.Background(), "test.Service", "TestMethod", map[string]string{}, nil)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !containsString(err.Error(), "unexpected status 500") {
		t.Errorf("expected error to contain 'unexpected status 500', got %q", err.Error())
	}
}

func TestDoConnectRPCStream_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/connect+json" {
			t.Errorf("expected Content-Type application/connect+json, got %q", r.Header.Get("Content-Type"))
		}

		// Read envelope-framed request
		var req map[string]string
		_ = readEnvelopeRequest(r, &req)

		w.Header().Set("Content-Type", "application/connect+json")
		w.WriteHeader(http.StatusOK)

		// Send envelope
		env, _ := EncodeEnvelope(EnvelopeFlagNone, map[string]string{"data": "test"})
		_, _ = w.Write(env)

		// Send end-stream envelope
		endEnv, _ := EncodeEnvelope(EnvelopeFlagEndStream, StreamTrailer{})
		_, _ = w.Write(endEnv)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
	})

	reader, err := client.doConnectRPCStream(context.Background(), "test.Service", "TestMethod", map[string]string{"request": "data"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer reader.Close()

	// Read first envelope
	env, err := DecodeEnvelope(reader)
	if err != nil {
		t.Fatalf("failed to decode envelope: %v", err)
	}

	if env.IsEndStream() {
		t.Error("expected non-end-stream envelope")
	}

	var data map[string]string
	if err := env.UnmarshalPayload(&data); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if data["data"] != "test" {
		t.Errorf("expected data=test, got %q", data["data"])
	}
}

func TestDoConnectRPCStream_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
	})

	_, err := client.doConnectRPCStream(context.Background(), "test.Service", "TestMethod", map[string]string{})

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !containsString(err.Error(), "unexpected status 401") {
		t.Errorf("expected error to contain 'unexpected status 401', got %q", err.Error())
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) &&
		(s[:len(substr)] == substr || containsString(s[1:], substr)))
}
