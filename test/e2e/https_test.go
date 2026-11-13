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

package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/e2bgateway/e2bgateway/internal/api/dto"
	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/server"
)

// httpsGateway drives a running HTTPS gateway instance.
type httpsGateway struct {
	baseURL  string
	certPath string
	keyPath  string
	rootCAs  *x509.CertPool
	client   *http.Client
}

// generateHTTPSCertPair writes a self-signed TLS certificate for localhost
// into dir and returns the certificate and key file paths.
func generateHTTPSCertPair(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating private key: %v", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generating serial number: %v", err)
	}

	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}

	certPath = filepath.Join(dir, "tls.crt")
	keyPath = filepath.Join(dir, "tls.key")

	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("writing certificate: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshaling private key: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("writing private key: %v", err)
	}

	return certPath, keyPath
}

// freeTLSPort reserves an ephemeral port and releases it for reuse by the
// test server. The reserve-then-release window is acceptable for tests; an
// injected listener on :0 would avoid it if flakiness ever appears.
func freeTLSPort(t *testing.T) int {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a free port: %v", err)
	}
	defer l.Close()

	return l.Addr().(*net.TCPAddr).Port
}

// startHTTPSGateway boots a real gateway server (mock backend) whose HTTPS
// listener terminates TLS with the given certificate pair, and returns a
// fixture with an HTTPS client trusting the certificate.
func startHTTPSGateway(t *testing.T, certPath, keyPath string, reloadInterval time.Duration) *httpsGateway {
	t.Helper()

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(freeTLSPort(t)))

	cfg := &config.Config{
		Server: config.ServerConfig{
			HTTP:  config.HTTPConfig{Address: "127.0.0.1:0"},
			HTTPS: config.HTTPSConfig{Address: addr, CertFile: certPath, KeyFile: keyPath, ReloadInterval: reloadInterval},
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
	require.NoError(t, err, "creating server")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		require.NoError(t, srv.Stop(context.Background()), "stopping server")
	})

	go func() {
		_ = srv.Start(ctx)
	}()

	certPEM, err := os.ReadFile(certPath)
	require.NoError(t, err, "reading certificate")
	rootCAs := x509.NewCertPool()
	require.True(t, rootCAs.AppendCertsFromPEM(certPEM), "parsing self-signed certificate")

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    rootCAs,
				MinVersion: tls.VersionTLS12,
			},
		},
	}

	return &httpsGateway{
		baseURL:  "https://" + addr,
		certPath: certPath,
		keyPath:  keyPath,
		rootCAs:  rootCAs,
		client:   client,
	}
}

// waitUntilServing polls the gateway until an HTTPS request succeeds, so the
// listener startup does not race the first request.
func (g *httpsGateway) waitUntilServing(t *testing.T) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := g.client.Get(g.baseURL + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("HTTPS gateway did not become ready: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// doJSON performs a JSON request against the HTTPS gateway.
func (g *httpsGateway) doJSON(t *testing.T, method, path string, body any) *http.Response {
	t.Helper()

	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(t, err, "marshaling body")
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, g.baseURL+path, reqBody)
	require.NoError(t, err, "creating request")
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	require.NoError(t, err, "%s %s", method, path)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// certSerialFromPEMFile parses the PEM certificate at path and returns its serial.
func certSerialFromPEMFile(t *testing.T, path string) *big.Int {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err, "reading certificate file")
	block, _ := pem.Decode(data)
	require.NotNil(t, block, "decoding certificate PEM")
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err, "parsing certificate")
	return cert.SerialNumber
}

// tlsPeerSerial completes a raw TLS handshake and returns the peer certificate serial.
func tlsPeerSerial(t *testing.T, addr string, rootCAs *x509.CertPool) *big.Int {
	t.Helper()

	dialer := &net.Dialer{Timeout: 2 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		RootCAs:    rootCAs,
		MinVersion: tls.VersionTLS12,
	})
	require.NoError(t, err, "TLS handshake")
	defer conn.Close()

	certs := conn.ConnectionState().PeerCertificates
	require.NotEmpty(t, certs, "no peer certificate presented")
	return certs[0].SerialNumber
}

// waitForTLSPeerSerial polls TLS handshakes until the server presents want.
func waitForTLSPeerSerial(t *testing.T, addr string, rootCAs *x509.CertPool, want *big.Int) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		serial := tlsPeerSerial(t, addr, rootCAs)
		if serial.Cmp(want) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for TLS peer serial %s, last %s", want, serial)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestHTTPS_APIOverTLS drives the E2B sandbox API end to end through the
// gateway's real HTTPS listener: the control plane must behave identically
// over TLS, including health endpoints and a full sandbox lifecycle.
func TestHTTPS_APIOverTLS(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := generateHTTPSCertPair(t, dir)
	gw := startHTTPSGateway(t, certPath, keyPath, 0)
	gw.waitUntilServing(t)

	// Health endpoints over TLS.
	healthResp := gw.doJSON(t, http.MethodGet, "/healthz", nil)
	require.Equal(t, http.StatusOK, healthResp.StatusCode)

	readyResp := gw.doJSON(t, http.MethodGet, "/readyz", nil)
	require.Equal(t, http.StatusOK, readyResp.StatusCode)

	// Sandbox lifecycle over TLS: create -> get -> kill.
	createResp := gw.doJSON(t, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	require.Equal(t, http.StatusCreated, createResp.StatusCode)
	var created dto.SandboxCreateResponse
	decodeJSON(t, createResp, &created)
	require.NotEmpty(t, created.SandboxID)

	getResp := gw.doJSON(t, http.MethodGet, "/sandboxes/"+created.SandboxID, nil)
	require.Equal(t, http.StatusOK, getResp.StatusCode)
	var info dto.SandboxInfo
	decodeJSON(t, getResp, &info)
	require.Equal(t, created.SandboxID, info.SandboxID)
	require.Equal(t, "running", info.State)

	killResp := gw.doJSON(t, http.MethodDelete, "/sandboxes/"+created.SandboxID, nil)
	require.Equal(t, http.StatusNoContent, killResp.StatusCode)
}

// TestHTTPS_CertRotation swaps the certificate files under a running gateway
// and verifies, over real TLS handshakes, that the new certificate is served
// without a restart.
func TestHTTPS_CertRotation(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := generateHTTPSCertPair(t, dir)
	gw := startHTTPSGateway(t, certPath, keyPath, 50*time.Millisecond)
	gw.waitUntilServing(t)

	addr := gw.baseURL[len("https://"):]
	serialA := tlsPeerSerial(t, addr, gw.rootCAs)

	// Rotate: overwrite both files with a freshly generated pair.
	certPath2, keyPath2 := generateHTTPSCertPair(t, dir)
	require.Equal(t, certPath, certPath2)
	require.Equal(t, keyPath, keyPath2)
	serialB := certSerialFromPEMFile(t, certPath)
	require.NotEqual(t, serialA, serialB, "expected the rotated certificate to differ")

	newCertPEM, err := os.ReadFile(certPath)
	require.NoError(t, err, "reading rotated certificate")
	require.True(t, gw.rootCAs.AppendCertsFromPEM(newCertPEM), "parsing rotated self-signed certificate")

	waitForTLSPeerSerial(t, addr, gw.rootCAs, serialB)

	// The API keeps serving over TLS after the rotation.
	healthResp := gw.doJSON(t, http.MethodGet, "/healthz", nil)
	require.Equal(t, http.StatusOK, healthResp.StatusCode)
}

// TestServerHTTPSStopCancelsCertWatcher verifies that Stop terminates the
// certificate watcher even when Start was called with a context that is never
// cancelled: after Stop, certificate rotation must not be picked up.
func TestServerHTTPSStopCancelsCertWatcher(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := generateHTTPSCertPair(t, dir)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(freeTLSPort(t)))

	cfg := &config.Config{
		Server: config.ServerConfig{
			HTTP:  config.HTTPConfig{Address: "127.0.0.1:0"},
			HTTPS: config.HTTPSConfig{Address: addr, CertFile: certPath, KeyFile: keyPath, ReloadInterval: 10 * time.Millisecond},
		},
		Backends: []config.BackendConfig{
			{Name: "mock", Type: "mock", Enabled: true},
		},
		Routing: config.RoutingConfig{
			DefaultBackend: "mock",
			Strategy:       "static",
		},
	}

	// Capture the default slog logger before New: the initial certificate
	// load in New logs through it.
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	srv, err := server.New(cfg)
	require.NoError(t, err, "creating server")

	// A context that is never cancelled during the test: only Stop can stop
	// the watcher. Cancelling the parent context in the cleanup releases the
	// Start goroutine after the test finishes.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Start(ctx) }()

	// Wait until the HTTPS listener accepts connections: the watcher's
	// cancellation is registered before the listener starts, so reaching it
	// guarantees Stop will observe the cancel function.
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", addr, time.Second)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("HTTPS listener did not come up: %v", dialErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	require.NoError(t, srv.Stop(context.Background()))

	// The initial load happened in New; no further reloads may be logged.
	base := strings.Count(buf.String(), "TLS certificate reloaded")
	require.Equal(t, 1, base, "expected exactly the initial certificate load")

	// Rotate the certificate: the stopped watcher must not pick it up.
	generateHTTPSCertPair(t, dir)
	time.Sleep(300 * time.Millisecond) // ~30 reload intervals

	require.Equal(t, base, strings.Count(buf.String(), "TLS certificate reloaded"),
		"watcher reloaded the certificate after the server was stopped")
}
