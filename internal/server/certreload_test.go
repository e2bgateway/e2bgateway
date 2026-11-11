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

package server

import (
	"context"
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
	"sync"
	"testing"
	"time"
)

// writeTestCert generates a self-signed certificate pair at the given paths.
func writeTestCert(t *testing.T, certPath, keyPath string) *x509.Certificate {
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

	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing generated certificate: %v", err)
	}
	return parsed
}

func newTestReloader(t *testing.T, dir string, interval time.Duration) (*certReloader, string, string) {
	t.Helper()

	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	writeTestCert(t, certPath, keyPath)

	r, err := newCertReloader(certPath, keyPath, interval)
	if err != nil {
		t.Fatalf("creating cert reloader: %v", err)
	}
	return r, certPath, keyPath
}

func servedSerial(t *testing.T, r *certReloader) *big.Int {
	t.Helper()

	cert, err := r.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if cert == nil || len(cert.Certificate) == 0 {
		t.Fatal("GetCertificate returned an empty certificate")
	}
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parsing served certificate: %v", err)
	}
	return parsed.SerialNumber
}

func waitForSerial(t *testing.T, r *certReloader, want *big.Int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if servedSerial(t, r).Cmp(want) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for certificate serial %s", want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNewCertReloaderLoadsCertificate(t *testing.T) {
	dir := t.TempDir()
	r, _, _ := newTestReloader(t, dir, time.Hour)

	cert, err := r.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if cert == nil || len(cert.Certificate) == 0 {
		t.Fatal("expected a loaded certificate")
	}
}

func TestNewCertReloaderFailsOnMissingFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := newCertReloader(filepath.Join(dir, "missing.crt"), filepath.Join(dir, "missing.key"), time.Second); err == nil {
		t.Fatal("expected an error for missing certificate files")
	}
}

func TestCertReloaderReloadsChangedFiles(t *testing.T) {
	dir := t.TempDir()
	r, certPath, keyPath := newTestReloader(t, dir, 20*time.Millisecond)
	serialA := servedSerial(t, r)

	go r.watch(t.Context())

	time.Sleep(10 * time.Millisecond)
	rotated := writeTestCert(t, certPath, keyPath)
	if rotated.SerialNumber.Cmp(serialA) == 0 {
		t.Fatal("expected the rotated certificate to have a different serial number")
	}

	waitForSerial(t, r, rotated.SerialNumber)
}

func TestCertReloaderKeepsCertificateOnReloadFailure(t *testing.T) {
	dir := t.TempDir()
	r, certPath, keyPath := newTestReloader(t, dir, 20*time.Millisecond)
	serialA := servedSerial(t, r)

	go r.watch(t.Context())

	if err := os.WriteFile(certPath, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("writing invalid certificate: %v", err)
	}
	time.Sleep(150 * time.Millisecond)

	if got := servedSerial(t, r); got.Cmp(serialA) != 0 {
		t.Fatalf("served serial changed after a failed reload: got %s, want %s", got, serialA)
	}

	time.Sleep(10 * time.Millisecond)
	fixed := writeTestCert(t, certPath, keyPath)
	waitForSerial(t, r, fixed.SerialNumber)
}

func TestCertReloaderZeroIntervalDisablesWatch(t *testing.T) {
	dir := t.TempDir()
	r, certPath, keyPath := newTestReloader(t, dir, 0)
	serialA := servedSerial(t, r)

	done := make(chan struct{})
	go func() {
		r.watch(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watch did not return when the reload interval is zero")
	}

	time.Sleep(10 * time.Millisecond)
	writeTestCert(t, certPath, keyPath)
	time.Sleep(100 * time.Millisecond)

	if got := servedSerial(t, r); got.Cmp(serialA) != 0 {
		t.Fatalf("served serial changed with hot reload disabled: got %s, want %s", got, serialA)
	}
}

func TestCertReloaderConcurrentGetCertificate(t *testing.T) {
	dir := t.TempDir()
	r, certPath, keyPath := newTestReloader(t, dir, 10*time.Millisecond)

	go r.watch(t.Context())

	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	deadline := time.Now().Add(200 * time.Millisecond)
	for range 8 {
		wg.Go(func() {
			for time.Now().Before(deadline) {
				cert, err := r.GetCertificate(&tls.ClientHelloInfo{})
				if err != nil {
					errCh <- err
					return
				}
				if cert == nil || len(cert.Certificate) == 0 {
					errCh <- errors.New("empty certificate")
					return
				}
			}
		})
	}

	for range 2 {
		time.Sleep(20 * time.Millisecond)
		writeTestCert(t, certPath, keyPath)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("GetCertificate: %v", err)
	}
}
