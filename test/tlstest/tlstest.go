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

// Package tlstest provides self-signed certificate, listener and TLS
// handshake helpers shared by the gateway's HTTPS tests.
package tlstest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// GenerateCert creates a self-signed ECDSA P-256 certificate for localhost
// and 127.0.0.1 with the given validity window. It returns the PEM-encoded
// certificate and private key together with the parsed certificate.
func GenerateCert(t testing.TB, notBefore, notAfter time.Time) (certPEM, keyPEM []byte, leaf *x509.Certificate) {
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
		NotBefore:             notBefore,
		NotAfter:              notAfter,
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

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshaling private key: %v", err)
	}

	leaf, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing generated certificate: %v", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		leaf
}

// WriteCert generates a certificate pair with the given validity window,
// writes it to certPath and keyPath (certificate first), and returns the
// parsed certificate.
func WriteCert(t testing.TB, certPath, keyPath string, notBefore, notAfter time.Time) *x509.Certificate {
	t.Helper()

	certPEM, keyPEM, leaf := GenerateCert(t, notBefore, notAfter)
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("writing certificate: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("writing private key: %v", err)
	}
	return leaf
}

// WriteValidCert writes a certificate pair that is valid from an hour ago
// until a day from now to dir/tls.crt and dir/tls.key, and returns the two
// paths. Calling it again for the same dir rotates the pair in place.
func WriteValidCert(t testing.TB, dir string) (certPath, keyPath string) {
	t.Helper()

	certPath = filepath.Join(dir, "tls.crt")
	keyPath = filepath.Join(dir, "tls.key")
	now := time.Now()
	WriteCert(t, certPath, keyPath, now.Add(-time.Hour), now.Add(24*time.Hour))
	return certPath, keyPath
}

// TrustCertFile adds the PEM certificate at path to roots, so TLS clients
// using roots accept the self-signed certificate.
func TrustCertFile(t testing.TB, roots *x509.CertPool, path string) {
	t.Helper()

	certPEM, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading certificate: %v", err)
	}
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatalf("no certificate found in %s", path)
	}
}

// SerialFromFile returns the serial number of the PEM certificate at path.
func SerialFromFile(t testing.TB, path string) *big.Int {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading certificate: %v", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatalf("decoding certificate PEM from %s", path)
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parsing certificate: %v", err)
	}
	return cert.SerialNumber
}

// FreeAddr reserves an ephemeral loopback port and releases it for reuse by
// a test server, returning "127.0.0.1:<port>". Reserve-then-release has a
// small TOCTOU window (another process can bind the port between close and
// server start); acceptable for tests — an injected listener on :0 would
// avoid it if flakiness ever appears.
func FreeAddr(t testing.TB) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a free port: %v", err)
	}
	defer l.Close()

	return l.Addr().String()
}

// WaitForListener polls until a TCP connection to addr succeeds, failing the
// test after 10 seconds.
func WaitForListener(t testing.TB, addr string) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("listener at %s did not come up: %v", addr, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// PeerSerial completes a TLS handshake with addr, trusting roots, and returns
// the serial number of the certificate the server presented.
func PeerSerial(addr string, roots *x509.CertPool) (*big.Int, error) {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		RootCAs:    roots,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, errors.New("no peer certificate presented")
	}
	return certs[0].SerialNumber, nil
}

// WaitForPeerSerial polls TLS handshakes with addr until the server presents
// the certificate with serial want, failing the test after 10 seconds.
func WaitForPeerSerial(t testing.TB, addr string, roots *x509.CertPool, want *big.Int) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		serial, err := PeerSerial(addr, roots)
		if err == nil && serial.Cmp(want) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for TLS peer serial %s (last serial: %v, last error: %v)", want, serial, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
