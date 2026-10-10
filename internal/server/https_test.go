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
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/server"
)

// generateSelfSignedCert writes a self-signed TLS certificate for localhost
// into dir and returns the certificate and key file paths.
func generateSelfSignedCert(t *testing.T, dir string) (certPath, keyPath string) {
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

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("writing certificate: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshaling private key: %v", err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("writing private key: %v", err)
	}

	return certPath, keyPath
}

// freePort reserves an ephemeral port and releases it for reuse by the test server.
func freePort(t *testing.T) int {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a free port: %v", err)
	}
	defer l.Close()

	return l.Addr().(*net.TCPAddr).Port
}

func TestServerHTTPSListener(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := generateSelfSignedCert(t, dir)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(freePort(t)))

	cfg := &config.Config{
		Server: config.ServerConfig{
			HTTP:  config.HTTPConfig{Address: "127.0.0.1:0"},
			HTTPS: config.HTTPSConfig{Address: addr, CertFile: certPath, KeyFile: keyPath},
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("reading certificate: %v", err)
	}

	rootCAs := x509.NewCertPool()
	if !rootCAs.AppendCertsFromPEM(certPEM) {
		t.Fatal("failed to parse self-signed certificate")
	}

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

	cancel()
	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("failed to stop server: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("server error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for server to return")
	}
}
