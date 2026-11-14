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
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeTestCert generates a self-signed certificate pair at the given paths.
func writeTestCert(t *testing.T, certPath, keyPath string) *x509.Certificate {
	t.Helper()
	return writeTestCertWithValidity(t, certPath, keyPath, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
}

// writeTestCertWithValidity generates a self-signed certificate pair with the given validity window.
func writeTestCertWithValidity(t *testing.T, certPath, keyPath string, notBefore, notAfter time.Time) *x509.Certificate {
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

// inMemoryCertWithValidity builds a tls.Certificate with a parsed leaf without
// touching the filesystem, so tests can simulate a served certificate crossing
// an expiry threshold between reloads.
func inMemoryCertWithValidity(t *testing.T, notBefore, notAfter time.Time) tls.Certificate {
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

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing certificate: %v", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
		Leaf:        leaf,
	}
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
	waitForSerialWithin(t, r, want, 5*time.Second)
}

func waitForSerialWithin(t *testing.T, r *certReloader, want *big.Int, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
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

// captureDefaultLogger redirects the default slog logger into a buffer for the
// duration of the test and restores the previous logger on cleanup.
func captureDefaultLogger(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
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

func TestCertReloaderWarnsOnExpiredCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	expired := writeTestCertWithValidity(t, certPath, keyPath, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))

	buf := captureDefaultLogger(t)

	r, err := newCertReloader(certPath, keyPath, 0)
	if err != nil {
		t.Fatalf("newCertReloader must succeed with an expired certificate: %v", err)
	}

	cert, err := r.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if cert == nil || len(cert.Certificate) == 0 {
		t.Fatal("expected the expired certificate to be served")
	}
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parsing served certificate: %v", err)
	}
	if parsed.SerialNumber.Cmp(expired.SerialNumber) != 0 {
		t.Fatalf("served certificate is not the expired one: got serial %s, want %s", parsed.SerialNumber, expired.SerialNumber)
	}

	logged := buf.String()
	if !strings.Contains(logged, "TLS certificate has expired and will be served until replaced") {
		t.Errorf("expected expired-certificate warning in logs, got: %s", logged)
	}
	if !strings.Contains(logged, expired.NotAfter.Format("2006-01-02T15:04:05")) {
		t.Errorf("expected notAfter %s in logs, got: %s", expired.NotAfter, logged)
	}
}

func TestCertReloaderWarnsOnNearExpiryCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	soon := writeTestCertWithValidity(t, certPath, keyPath, time.Now().Add(-time.Hour), time.Now().Add(3*24*time.Hour))

	buf := captureDefaultLogger(t)

	r, err := newCertReloader(certPath, keyPath, 0)
	if err != nil {
		t.Fatalf("newCertReloader must succeed with a near-expiry certificate: %v", err)
	}
	if _, err := r.GetCertificate(&tls.ClientHelloInfo{}); err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}

	logged := buf.String()
	if !strings.Contains(logged, "TLS certificate expires soon") {
		t.Errorf("expected near-expiry warning in logs, got: %s", logged)
	}
	if !strings.Contains(logged, soon.NotAfter.Format("2006-01-02T15:04:05")) {
		t.Errorf("expected notAfter %s in logs, got: %s", soon.NotAfter, logged)
	}
}

func TestCertReloaderNoWarnOnFreshCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	writeTestCertWithValidity(t, certPath, keyPath, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour))

	buf := captureDefaultLogger(t)

	r, err := newCertReloader(certPath, keyPath, 0)
	if err != nil {
		t.Fatalf("newCertReloader: %v", err)
	}
	if _, err := r.GetCertificate(&tls.ClientHelloInfo{}); err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}

	logged := buf.String()
	for _, unwanted := range []string{
		"TLS certificate has expired and will be served until replaced",
		"TLS certificate expires soon",
	} {
		if strings.Contains(logged, unwanted) {
			t.Errorf("did not expect warning %q for a fresh certificate, got: %s", unwanted, logged)
		}
	}
}

// assertLogCount fails the test when the captured slog output does not contain
// exactly want occurrences of msg.
func assertLogCount(t *testing.T, buf *bytes.Buffer, msg string, want int) {
	t.Helper()
	if got := strings.Count(buf.String(), msg); got != want {
		t.Fatalf("logs contain %d occurrences of %q, want %d; logs:\n%s", got, msg, want, buf.String())
	}
}

func TestCertReloaderWarnsOncePerThresholdOnTicks(t *testing.T) {
	t.Run("near expiry threshold", func(t *testing.T) {
		dir := t.TempDir()
		certPath := filepath.Join(dir, "tls.crt")
		keyPath := filepath.Join(dir, "tls.key")
		writeTestCertWithValidity(t, certPath, keyPath, time.Now().Add(-time.Hour), time.Now().Add(3*24*time.Hour))

		buf := captureDefaultLogger(t)

		r, err := newCertReloader(certPath, keyPath, 0)
		if err != nil {
			t.Fatalf("newCertReloader: %v", err)
		}
		assertLogCount(t, buf, "TLS certificate expires soon", 1)

		// Repeated tick re-checks must not duplicate the warning.
		r.checkExpiry()
		r.checkExpiry()
		assertLogCount(t, buf, "TLS certificate expires soon", 1)

		// A successful reload with a fresh cert resets the dedup state.
		writeTestCertWithValidity(t, certPath, keyPath, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour))
		if err := r.reload(); err != nil {
			t.Fatalf("reload with fresh certificate: %v", err)
		}
		assertLogCount(t, buf, "TLS certificate expires soon", 1)

		// A successful reload with another near-expiry cert warns again for
		// the new certificate state.
		writeTestCertWithValidity(t, certPath, keyPath, time.Now().Add(-time.Hour), time.Now().Add(3*24*time.Hour))
		if err := r.reload(); err != nil {
			t.Fatalf("reload with near-expiry certificate: %v", err)
		}
		assertLogCount(t, buf, "TLS certificate expires soon", 2)

		// The tick re-check stays deduplicated for the new certificate.
		r.checkExpiry()
		assertLogCount(t, buf, "TLS certificate expires soon", 2)
	})

	t.Run("expired threshold", func(t *testing.T) {
		dir := t.TempDir()
		certPath := filepath.Join(dir, "tls.crt")
		keyPath := filepath.Join(dir, "tls.key")
		writeTestCertWithValidity(t, certPath, keyPath, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))

		buf := captureDefaultLogger(t)

		r, err := newCertReloader(certPath, keyPath, 0)
		if err != nil {
			t.Fatalf("newCertReloader: %v", err)
		}
		assertLogCount(t, buf, "TLS certificate has expired and will be served until replaced", 1)

		r.checkExpiry()
		r.checkExpiry()
		assertLogCount(t, buf, "TLS certificate has expired and will be served until replaced", 1)
	})
}

// TestCertReloaderTickDetectsThresholdCrossing covers the tick path: a
// certificate that is fresh when loaded but crosses a warning threshold before
// the next check must be caught by checkExpiry and warned about exactly once.
func TestCertReloaderTickDetectsThresholdCrossing(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	writeTestCertWithValidity(t, certPath, keyPath, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour))

	buf := captureDefaultLogger(t)

	// The watcher is never started; ticks are driven by calling checkExpiry
	// directly, keeping the test fully deterministic.
	r, err := newCertReloader(certPath, keyPath, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("newCertReloader: %v", err)
	}

	assertLogCount(t, buf, "TLS certificate expires soon", 0)
	assertLogCount(t, buf, "TLS certificate has expired and will be served until replaced", 0)

	t.Run("near expiry threshold crossing", func(t *testing.T) {
		crossed := inMemoryCertWithValidity(t, time.Now().Add(-time.Hour), time.Now().Add(3*24*time.Hour))
		r.current.Store(&crossed)

		r.checkExpiry()
		assertLogCount(t, buf, "TLS certificate expires soon", 1)

		r.checkExpiry()
		r.checkExpiry()
		assertLogCount(t, buf, "TLS certificate expires soon", 1)
	})

	t.Run("expired threshold crossing", func(t *testing.T) {
		crossed := inMemoryCertWithValidity(t, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))
		r.current.Store(&crossed)

		r.checkExpiry()
		assertLogCount(t, buf, "TLS certificate has expired and will be served until replaced", 1)

		r.checkExpiry()
		assertLogCount(t, buf, "TLS certificate has expired and will be served until replaced", 1)
	})
}

func TestCertReloaderSnapshotNotAdvancedOnFailedLoad(t *testing.T) {
	dir := t.TempDir()
	r, certPath, keyPath := newTestReloader(t, dir, 20*time.Millisecond)
	serialA := servedSerial(t, r)

	r.mu.Lock()
	origCertDigest, origKeyDigest := r.certDigest, r.keyDigest
	r.mu.Unlock()

	if err := os.WriteFile(certPath, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("writing invalid certificate: %v", err)
	}
	if err := r.reload(); err == nil {
		t.Fatal("expected reload to fail with an invalid certificate")
	}

	if got := servedSerial(t, r); got.Cmp(serialA) != 0 {
		t.Fatalf("served serial changed after a failed reload: got %s, want %s", got, serialA)
	}

	r.mu.Lock()
	digestsUnchanged := r.certDigest == origCertDigest && r.keyDigest == origKeyDigest
	r.mu.Unlock()
	if !digestsUnchanged {
		t.Fatal("content digests advanced even though the reload failed")
	}

	go r.watch(t.Context())

	fixed := writeTestCert(t, certPath, keyPath)
	waitForSerial(t, r, fixed.SerialNumber)
}

// testCertPEM generates a self-signed certificate pair and returns the PEM
// buffers along with the parsed certificate, without touching the filesystem.
func testCertPEM(t *testing.T) (certPEM, keyPEM []byte, leaf *x509.Certificate) {
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

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshaling private key: %v", err)
	}

	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing generated certificate: %v", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		parsed
}

// padToEqualLength appends trailing newlines to the shorter buffer until both
// buffers have the same length. PEM parsers ignore trailing whitespace, so
// this keeps both certificates valid while forcing identical file sizes.
func padToEqualLength(a, b []byte) ([]byte, []byte) {
	for len(a) < len(b) {
		a = append(a, '\n')
	}
	for len(b) < len(a) {
		b = append(b, '\n')
	}
	return a, b
}

// TestCertReloaderDetectsContentChangeWithPreservedMetadata is a regression
// test for rotation flows that preserve file metadata (cp -p, tar extraction):
// the new certificate content must be picked up even when the mtime is
// unchanged. Both certificate pairs are padded to identical file sizes, so
// size-based heuristics cannot explain a pass and only content-digest
// detection can flip the result.
func TestCertReloaderDetectsContentChangeWithPreservedMetadata(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")

	certAPEM, keyAPEM, leafA := testCertPEM(t)
	certBPEM, keyBPEM, leafB := testCertPEM(t)
	if leafA.SerialNumber.Cmp(leafB.SerialNumber) == 0 {
		t.Fatal("expected the two generated certificates to have different serial numbers")
	}

	// Force identical file sizes so the test isolates content-digest change
	// detection from any size-based shortcut.
	certAPEM, certBPEM = padToEqualLength(certAPEM, certBPEM)
	keyAPEM, keyBPEM = padToEqualLength(keyAPEM, keyBPEM)

	if err := os.WriteFile(certPath, certAPEM, 0o600); err != nil {
		t.Fatalf("writing certificate: %v", err)
	}
	if err := os.WriteFile(keyPath, keyAPEM, 0o600); err != nil {
		t.Fatalf("writing private key: %v", err)
	}

	r, err := newCertReloader(certPath, keyPath, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("creating cert reloader: %v", err)
	}

	certInfoA, err := os.Stat(certPath)
	if err != nil {
		t.Fatalf("stating certificate: %v", err)
	}
	keyInfoA, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stating private key: %v", err)
	}

	// Write the replacement pair and restore cert A's timestamps on both
	// files BEFORE starting the watcher, mimicking a metadata-preserving
	// rotation (cp -p, tar extraction): only the content differs, and the
	// watcher's first observation already has the final file metadata.
	if err := os.WriteFile(certPath, certBPEM, 0o600); err != nil {
		t.Fatalf("writing rotated certificate: %v", err)
	}
	if err := os.WriteFile(keyPath, keyBPEM, 0o600); err != nil {
		t.Fatalf("writing rotated private key: %v", err)
	}
	if err := os.Chtimes(certPath, certInfoA.ModTime(), certInfoA.ModTime()); err != nil {
		t.Fatalf("restoring certificate mtime: %v", err)
	}
	if err := os.Chtimes(keyPath, keyInfoA.ModTime(), keyInfoA.ModTime()); err != nil {
		t.Fatalf("restoring key mtime: %v", err)
	}

	certInfoB, err := os.Stat(certPath)
	if err != nil {
		t.Fatalf("stating rotated certificate: %v", err)
	}
	keyInfoB, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stating rotated private key: %v", err)
	}
	if certInfoB.Size() != certInfoA.Size() || keyInfoB.Size() != keyInfoA.Size() {
		t.Fatalf("rotation fixture must preserve file sizes: cert %d vs %d, key %d vs %d",
			certInfoB.Size(), certInfoA.Size(), keyInfoB.Size(), keyInfoA.Size())
	}

	go r.watch(t.Context())

	waitForSerialWithin(t, r, leafB.SerialNumber, 3*time.Second)
}

// TestCertReloaderDedupesReloadFailureWarnings verifies that repeated
// identical reload failures (unreadable files or the same invalid content)
// warn only once, a distinct failure warns again, and a successful load
// re-arms the dedup so a later recurrence warns once more.
func TestCertReloaderDedupesReloadFailureWarnings(t *testing.T) {
	dir := t.TempDir()
	r, certPath, keyPath := newTestReloader(t, dir, 0)

	buf := captureDefaultLogger(t)
	const failureMsg = "TLS certificate reload failed; keeping previous certificate"

	bad1 := []byte("not a certificate")
	if err := os.WriteFile(certPath, bad1, 0o600); err != nil {
		t.Fatalf("writing invalid certificate: %v", err)
	}
	if err := r.reload(); err == nil {
		t.Fatal("expected reload to fail with an invalid certificate")
	}
	assertLogCount(t, buf, failureMsg, 1)

	// The same failing content must not warn again on subsequent ticks.
	if err := r.reload(); err == nil {
		t.Fatal("expected reload to fail again with unchanged invalid content")
	}
	assertLogCount(t, buf, failureMsg, 1)

	// Different failing content is a distinct failure and warns again.
	if err := os.WriteFile(certPath, []byte("still not a certificate"), 0o600); err != nil {
		t.Fatalf("writing second invalid certificate: %v", err)
	}
	if err := r.reload(); err == nil {
		t.Fatal("expected reload to fail with the second invalid certificate")
	}
	assertLogCount(t, buf, failureMsg, 2)

	// An unreadable key file is another distinct failure, and repeats of it
	// are deduplicated as well.
	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("removing private key: %v", err)
	}
	if err := r.reload(); err == nil {
		t.Fatal("expected reload to fail with a missing key file")
	}
	assertLogCount(t, buf, failureMsg, 3)
	if err := r.reload(); err == nil {
		t.Fatal("expected reload to keep failing with a missing key file")
	}
	assertLogCount(t, buf, failureMsg, 3)

	// A successful load clears the dedup state...
	writeTestCert(t, certPath, keyPath)
	if err := r.reload(); err != nil {
		t.Fatalf("reload with restored certificate: %v", err)
	}
	assertLogCount(t, buf, "TLS certificate reloaded", 1)

	// ...so the original bad content warns once more.
	if err := os.WriteFile(certPath, bad1, 0o600); err != nil {
		t.Fatalf("re-writing invalid certificate: %v", err)
	}
	if err := r.reload(); err == nil {
		t.Fatal("expected reload to fail with the restored invalid content")
	}
	assertLogCount(t, buf, failureMsg, 4)
}

// TestCertReloaderClearsFailureStateOnRecoveredFiles verifies that a no-op
// reload (both files read successfully but unchanged since the last load)
// clears the failure-warning dedup, so a later recurrence of the same
// failure warns again.
func TestCertReloaderClearsFailureStateOnRecoveredFiles(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	writeTestCert(t, certPath, keyPath)

	r, err := newCertReloader(certPath, keyPath, 0)
	if err != nil {
		t.Fatalf("newCertReloader: %v", err)
	}

	buf := captureDefaultLogger(t)
	const failureMsg = "TLS certificate reload failed; keeping previous certificate"

	goodKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("reading key: %v", err)
	}

	// The key disappears: the read failure warns once.
	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("removing private key: %v", err)
	}
	if err := r.reload(); err == nil {
		t.Fatal("expected reload to fail with a missing key file")
	}
	assertLogCount(t, buf, failureMsg, 1)

	// The identical key is restored: both files read fine and their digests
	// are unchanged, so reload is a no-op — and it must clear the failure
	// state.
	if err := os.WriteFile(keyPath, goodKey, 0o600); err != nil {
		t.Fatalf("restoring private key: %v", err)
	}
	if err := r.reload(); err != nil {
		t.Fatalf("reload with restored key: %v", err)
	}
	assertLogCount(t, buf, "TLS certificate reloaded", 0)

	// The key disappears again with the identical error: the recovered
	// failure must warn again.
	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("removing private key again: %v", err)
	}
	if err := r.reload(); err == nil {
		t.Fatal("expected reload to fail again with a missing key file")
	}
	assertLogCount(t, buf, failureMsg, 2)
}

// TestCertReloaderReloadNoOpWhenUnchanged verifies that reload does nothing
// (and logs no reload) when the files are unchanged since the last
// successful load.
func TestCertReloaderReloadNoOpWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	r, _, _ := newTestReloader(t, dir, 0)

	buf := captureDefaultLogger(t)

	if err := r.reload(); err != nil {
		t.Fatalf("reload with unchanged files: %v", err)
	}
	assertLogCount(t, buf, "TLS certificate reloaded", 0)
}
