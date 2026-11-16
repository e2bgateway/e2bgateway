// Copyright The E2BGateway Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/server"
	"github.com/e2bgateway/e2bgateway/test/tlstest"
)

// newHTTPSTestServer creates a gateway server (mock backend) whose HTTPS
// listener on addr serves the given certificate pair.
func newHTTPSTestServer(t *testing.T, addr, certPath, keyPath string, reloadInterval time.Duration) *server.Server {
	t.Helper()

	cfg := &config.Config{
		Server: config.ServerConfig{
			HTTP: config.HTTPConfig{Address: "127.0.0.1:0"},
			HTTPS: config.HTTPSConfig{
				Address:        addr,
				CertFile:       certPath,
				KeyFile:        keyPath,
				ReloadInterval: reloadInterval,
			},
		},
		Backends: []config.BackendConfig{
			{Name: "mock", Type: "mock", Enabled: true},
		},
		Routing: config.RoutingConfig{
			DefaultBackend: "mock",
			Strategy:       "static",
		},
	}

	srv, err := server.New(cfg)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	return srv
}

// startServer runs srv in the background with a context that only the test
// cleanup cancels. Teardown is registered before the server starts, so a
// failing assertion never leaves the listeners or the certificate watcher
// running. The error drain is non-blocking: Start reports a listener failure
// as soon as it happens, but returns nil only after its context is cancelled,
// which may complete after the drain.
func startServer(t *testing.T, srv *server.Server) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	t.Cleanup(func() {
		cancel()
		_ = srv.Stop(context.Background())
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("server error: %v", err)
			}
		default:
		}
	})

	go func() {
		errCh <- srv.Start(ctx)
	}()
}

// lockedBuffer is a goroutine-safe bytes.Buffer. slog.SetDefault also routes
// the standard log package, so http.Server errors (such as TLS handshake
// errors caused by plain TCP readiness probes) are written from server
// goroutines while the test reads the captured output.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs redirects the default slog logger into a goroutine-safe buffer
// for the duration of the test. Cleanups run last-in first-out, so a server
// started after this call is stopped before the previous logger is restored.
func captureLogs(t *testing.T) *lockedBuffer {
	t.Helper()

	buf := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func TestServerHTTPSListener(t *testing.T) {
	certPath, keyPath := tlstest.WriteValidCert(t, t.TempDir())
	addr := tlstest.FreeAddr(t)
	startServer(t, newHTTPSTestServer(t, addr, certPath, keyPath, 0))

	rootCAs := x509.NewCertPool()
	tlstest.TrustCertFile(t, rootCAs, certPath)

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    rootCAs,
				MinVersion: tls.VersionTLS12,
			},
		},
	}

	url := "https://" + addr + "/healthz"
	deadline := time.Now().Add(10 * time.Second)
	var resp *http.Response
	for {
		var err error
		resp, err = client.Get(url)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("HTTPS request to %s did not succeed: %v", url, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer resp.Body.Close()

	if resp.TLS == nil {
		t.Fatal("expected a TLS connection")
	}
	if !resp.TLS.HandshakeComplete {
		t.Fatal("expected a completed TLS handshake")
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 on HTTPS /healthz, got %d", resp.StatusCode)
	}
}

func TestServerHTTPSListenerCertRotation(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := tlstest.WriteValidCert(t, dir)
	serialA := tlstest.SerialFromFile(t, certPath)
	addr := tlstest.FreeAddr(t)
	startServer(t, newHTTPSTestServer(t, addr, certPath, keyPath, 50*time.Millisecond))

	rootCAs := x509.NewCertPool()
	tlstest.TrustCertFile(t, rootCAs, certPath)
	tlstest.WaitForPeerSerial(t, addr, rootCAs, serialA)

	tlstest.WriteValidCert(t, dir)
	serialB := tlstest.SerialFromFile(t, certPath)
	if serialB.Cmp(serialA) == 0 {
		t.Fatal("expected the rotated certificate to have a different serial number")
	}
	tlstest.TrustCertFile(t, rootCAs, certPath)

	tlstest.WaitForPeerSerial(t, addr, rootCAs, serialB)
}

// TestServerHTTPSStopCancelsCertWatcher verifies that Stop terminates the
// certificate watcher even when Start was called with a context that is never
// cancelled: after Stop, certificate rotation must not be picked up.
func TestServerHTTPSStopCancelsCertWatcher(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := tlstest.WriteValidCert(t, dir)
	addr := tlstest.FreeAddr(t)

	// Capture logs before New: the initial certificate load in New logs
	// through the default logger.
	logs := captureLogs(t)
	srv := newHTTPSTestServer(t, addr, certPath, keyPath, 10*time.Millisecond)
	// Start's context is only cancelled by the test cleanup, so within the
	// test only Stop can stop the watcher.
	startServer(t, srv)

	// Start registers the watcher before it starts the HTTPS listener, so
	// once the listener accepts connections Stop is guaranteed to see it.
	tlstest.WaitForListener(t, addr)

	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Stop waits for the watcher to exit, so no reload can run from here on:
	// only the initial load in New is logged, and it is not a reload.
	const (
		loadedMsg   = "TLS certificate loaded"
		reloadedMsg = "TLS certificate reloaded"
	)
	assertLogs := func(when string) {
		t.Helper()
		logged := logs.String()
		if got := strings.Count(logged, loadedMsg); got != 1 {
			t.Fatalf("%s: logs contain %d occurrences of %q, want exactly the initial load; logs:\n%s", when, got, loadedMsg, logged)
		}
		if strings.Contains(logged, reloadedMsg) {
			t.Fatalf("%s: the watcher reloaded the certificate; logs:\n%s", when, logged)
		}
	}
	assertLogs("after Stop")

	// Rotate the certificate: the stopped watcher must not pick it up.
	tlstest.WriteValidCert(t, dir)
	time.Sleep(300 * time.Millisecond) // ~30 reload intervals
	assertLogs("after rotation")
}

// TestServerStopBeforeStartDisablesCertWatcher verifies that a Start that
// loses the race with Stop does not leave a certificate watcher running.
func TestServerStopBeforeStartDisablesCertWatcher(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := tlstest.WriteValidCert(t, dir)

	logs := captureLogs(t)
	srv := newHTTPSTestServer(t, tlstest.FreeAddr(t), certPath, keyPath, 10*time.Millisecond)
	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	startServer(t, srv)

	// A watcher registered after Stop is cancelled right away, so it must
	// not pick up the rotated certificate.
	tlstest.WriteValidCert(t, dir)
	time.Sleep(300 * time.Millisecond) // ~30 reload intervals
	if logged := logs.String(); strings.Contains(logged, "TLS certificate reloaded") {
		t.Fatalf("the watcher reloaded the certificate after Stop; logs:\n%s", logged)
	}
}
