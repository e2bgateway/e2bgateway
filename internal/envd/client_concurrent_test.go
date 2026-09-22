package envd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestClient_Concurrent(t *testing.T) {
	// Create a test server that responds with a valid JSON response (unary).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		_ = readJSONRequest(r, &req)
		writeJSONResponse(w, map[string]string{"result": "ok"})
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
		Timeout:     10 * time.Second,
	})

	const goroutines = 10
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var resp map[string]string
			err := client.doConnectRPC(
				context.Background(),
				"test.Service",
				"TestMethod",
				map[string]string{"key": "value"},
				&resp,
			)
			if err != nil {
				errCh <- err
				return
			}
			if resp["result"] != "ok" {
				errCh <- err
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent doConnectRPC error: %v", err)
	}
}

func TestClient_ContextCancellation(t *testing.T) {
	// Handler reads the full body then returns 200 OK.
	// If the client disconnects mid-read (context cancel), the read returns
	// an error and the handler returns early. Either way the handler exits
	// quickly so httptest.Server.Close() doesn't block.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Consume the request body; when the client closes the connection
		// this returns an error and we exit.
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
		Timeout:     30 * time.Second,
	})

	// Use a very short deadline so the context expires before or during the
	// HTTP round-trip, which forces httpClient.Do to return a context error.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	// Give the deadline a moment to be in the past.
	time.Sleep(10 * time.Millisecond)

	err := client.doConnectRPC(ctx, "test.Service", "TestMethod", map[string]string{}, nil)
	if err == nil {
		t.Fatal("expected error from expired context, got nil")
	}
}

func TestClient_AlreadyCancelledContext(t *testing.T) {
	// Verify that an already-canceled context returns immediately.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called with already-canceled context")
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:     server.URL,
		AccessToken: "test-token",
		SandboxID:   "test-sandbox",
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately before the call.

	err := client.doConnectRPC(ctx, "test.Service", "TestMethod", map[string]string{}, nil)
	if err == nil {
		t.Fatal("expected error from pre-canceled context, got nil")
	}
}
