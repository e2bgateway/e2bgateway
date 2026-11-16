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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/metrics"
	"github.com/e2bgateway/e2bgateway/test/tlstest"
)

// writeTestCert writes a self-signed certificate pair, valid from an hour ago
// until a day from now, to the given paths.
func writeTestCert(t *testing.T, certPath, keyPath string) *x509.Certificate {
	t.Helper()
	now := time.Now()
	return tlstest.WriteCert(t, certPath, keyPath, now.Add(-time.Hour), now.Add(24*time.Hour))
}

// inMemoryCertWithValidity builds a tls.Certificate with a parsed leaf without
// touching the filesystem, so tests can simulate a served certificate crossing
// an expiry threshold between reloads.
func inMemoryCertWithValidity(t *testing.T, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()

	certPEM, keyPEM, leaf := tlstest.GenerateCert(t, notBefore, notAfter)
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("parsing certificate pair: %v", err)
	}
	cert.Leaf = leaf
	return cert
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
	buf := captureDefaultLogger(t)

	dir := t.TempDir()
	r, _, _ := newTestReloader(t, dir, time.Hour)

	cert, err := r.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if cert == nil || len(cert.Certificate) == 0 {
		t.Fatal("expected a loaded certificate")
	}

	// The startup load is not a hot reload and must not be logged as one.
	assertLogCount(t, buf, "TLS certificate loaded", 1)
	assertLogCount(t, buf, "TLS certificate reloaded", 0)
}

func TestNewCertReloaderFailsOnMissingFiles(t *testing.T) {
	initTestMetrics(t)
	buf := captureDefaultLogger(t)

	dir := t.TempDir()
	if _, err := newCertReloader(filepath.Join(dir, "missing.crt"), filepath.Join(dir, "missing.key"), time.Second); err == nil {
		t.Fatal("expected an error for missing certificate files")
	}

	// A failed initial load aborts startup: there is no previous certificate
	// to keep, and it is not a failed hot reload.
	assertLogCount(t, buf, "keeping previous certificate", 0)
	if body := scrapeMetrics(t); strings.Contains(body, "e2bgw_tls_certificate_reloads_total") {
		t.Errorf("initial load failure must not be counted as a reload, got:\n%s", body)
	}
}

// TestClassifyExpiry pins the expiry status boundaries, including the
// precedence of expiry over not-yet-valid for an inverted validity window.
func TestClassifyExpiry(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name      string
		notBefore time.Time
		notAfter  time.Time
		want      expiryStatus
	}{
		{"fresh", now.Add(-time.Hour), now.Add(90 * 24 * time.Hour), expiryOK},
		{"valid from now", now, now.Add(90 * 24 * time.Hour), expiryOK},
		{"warn window ends exactly now", now.Add(-time.Hour), now.Add(certExpiryWarnWindow), expiryOK},
		{"inside the warn window", now.Add(-time.Hour), now.Add(certExpiryWarnWindow - time.Nanosecond), expiryNearExpiry},
		{"expires exactly now", now.Add(-time.Hour), now, expiryNearExpiry},
		{"expired", now.Add(-48 * time.Hour), now.Add(-24 * time.Hour), expiryExpired},
		{"not yet valid", now.Add(time.Hour), now.Add(90 * 24 * time.Hour), expiryNotYetValid},
		{"inverted window past NotAfter", now.Add(time.Hour), now.Add(-time.Hour), expiryExpired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leaf := &x509.Certificate{NotBefore: tt.notBefore, NotAfter: tt.notAfter}
			if got := classifyExpiry(leaf, now); got != tt.want {
				t.Errorf("classifyExpiry() = %d, want %d", got, tt.want)
			}
		})
	}
}

// mustReadFile returns the content of path, failing the test on error.
func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return data
}

// TestCertReloaderRetriesHalfRotatedPair verifies that a reload catching a
// two-step rotation half-way (the new certificate next to the old key)
// re-reads the files and serves the completed pair, without warning about or
// counting the transient mismatch as a failed reload.
func TestCertReloaderRetriesHalfRotatedPair(t *testing.T) {
	initTestMetrics(t)

	dir := t.TempDir()
	r, certPath, keyPath := newTestReloader(t, dir, 0)
	oldKey := mustReadFile(t, keyPath)
	rotated := writeTestCert(t, certPath, keyPath)
	newCert, newKey := mustReadFile(t, certPath), mustReadFile(t, keyPath)

	buf := captureDefaultLogger(t)
	reads := 0
	r.readFiles = func(string, string) ([]byte, []byte, error) {
		reads++
		if reads == 1 {
			return newCert, oldKey, nil
		}
		return newCert, newKey, nil
	}

	if err := r.reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reads != 2 {
		t.Fatalf("reload read the files %d times, want 2", reads)
	}
	if got := servedSerial(t, r); got.Cmp(rotated.SerialNumber) != 0 {
		t.Fatalf("served serial = %s, want the rotated %s", got, rotated.SerialNumber)
	}
	assertLogCount(t, buf, "TLS certificate reload failed", 0)
	assertLogCount(t, buf, "TLS certificate reloaded", 1)

	body := scrapeMetrics(t)
	if !strings.Contains(body, `e2bgw_tls_certificate_reloads_total{result="success"} 1`) {
		t.Errorf("expected one successful hot reload, got:\n%s", body)
	}
	if strings.Contains(body, `result="failure"`) {
		t.Errorf("a recovered half-rotated pair must not count as a failed reload, got:\n%s", body)
	}
}

// TestCertReloaderReportsPersistentMismatch verifies that a mismatched pair
// still on disk after the re-read fails the reload: the previous certificate
// stays served, and the failure is warned about and counted once.
func TestCertReloaderReportsPersistentMismatch(t *testing.T) {
	initTestMetrics(t)

	dir := t.TempDir()
	r, certPath, keyPath := newTestReloader(t, dir, 0)
	serialA := servedSerial(t, r)
	oldKey := mustReadFile(t, keyPath)
	writeTestCert(t, certPath, keyPath)
	newCert := mustReadFile(t, certPath)

	buf := captureDefaultLogger(t)
	reads := 0
	r.readFiles = func(string, string) ([]byte, []byte, error) {
		reads++
		return newCert, oldKey, nil
	}

	if err := r.reload(); err == nil {
		t.Fatal("expected reload to fail with a mismatched certificate and key")
	}
	if reads != 2 {
		t.Fatalf("reload read the files %d times, want 2 (one re-read)", reads)
	}
	if got := servedSerial(t, r); got.Cmp(serialA) != 0 {
		t.Fatalf("served serial changed after a failed reload: got %s, want %s", got, serialA)
	}
	assertLogCount(t, buf, "TLS certificate reload failed; keeping previous certificate", 1)
	if body := scrapeMetrics(t); !strings.Contains(body, `e2bgw_tls_certificate_reloads_total{result="failure"} 1`) {
		t.Errorf("expected one counted reload failure, got:\n%s", body)
	}
}

func TestCertReloaderReloadsChangedFiles(t *testing.T) {
	dir := t.TempDir()
	r, certPath, keyPath := newTestReloader(t, dir, 20*time.Millisecond)
	serialA := servedSerial(t, r)

	go r.watch(t.Context())

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
	expired := tlstest.WriteCert(t, certPath, keyPath, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))

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
	soon := tlstest.WriteCert(t, certPath, keyPath, time.Now().Add(-time.Hour), time.Now().Add(3*24*time.Hour))

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

func TestCertReloaderWarnsOnNotYetValidCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	future := tlstest.WriteCert(t, certPath, keyPath, time.Now().Add(48*time.Hour), time.Now().Add(90*24*time.Hour))

	buf := captureDefaultLogger(t)

	r, err := newCertReloader(certPath, keyPath, 0)
	if err != nil {
		t.Fatalf("newCertReloader must succeed with a not-yet-valid certificate: %v", err)
	}
	if _, err := r.GetCertificate(&tls.ClientHelloInfo{}); err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}

	logged := buf.String()
	if !strings.Contains(logged, "TLS certificate is not yet valid and will be served anyway") {
		t.Errorf("expected not-yet-valid warning in logs, got: %s", logged)
	}
	if !strings.Contains(logged, future.NotBefore.Format("2006-01-02T15:04:05")) {
		t.Errorf("expected notBefore %s in logs, got: %s", future.NotBefore, logged)
	}

	// Repeated tick re-checks must not duplicate the warning.
	r.checkExpiry()
	r.checkExpiry()
	assertLogCount(t, buf, "TLS certificate is not yet valid and will be served anyway", 1)
}

func TestCertReloaderNoWarnOnFreshCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	tlstest.WriteCert(t, certPath, keyPath, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour))

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
		"TLS certificate is not yet valid and will be served anyway",
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
		tlstest.WriteCert(t, certPath, keyPath, time.Now().Add(-time.Hour), time.Now().Add(3*24*time.Hour))

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
		tlstest.WriteCert(t, certPath, keyPath, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour))
		if err := r.reload(); err != nil {
			t.Fatalf("reload with fresh certificate: %v", err)
		}
		assertLogCount(t, buf, "TLS certificate expires soon", 1)

		// A successful reload with another near-expiry cert warns again for
		// the new certificate state.
		tlstest.WriteCert(t, certPath, keyPath, time.Now().Add(-time.Hour), time.Now().Add(3*24*time.Hour))
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
		tlstest.WriteCert(t, certPath, keyPath, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))

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
	tlstest.WriteCert(t, certPath, keyPath, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour))

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

	now := time.Now()
	certAPEM, keyAPEM, leafA := tlstest.GenerateCert(t, now.Add(-time.Hour), now.Add(24*time.Hour))
	certBPEM, keyBPEM, leafB := tlstest.GenerateCert(t, now.Add(-time.Hour), now.Add(24*time.Hour))
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

// initTestMetrics initializes the process-global metrics recorder for a test
// and restores the disabled state on cleanup.
func initTestMetrics(t *testing.T) {
	t.Helper()
	metrics.Reset()
	t.Cleanup(metrics.Reset)
	metrics.Init(config.MetricsConfig{Enabled: true, Path: "/metrics", Prefix: "e2bgw"}, metrics.Sources{})
}

// scrapeMetrics renders the current global metrics registry as text.
func scrapeMetrics(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

// TestCertReloaderMetricsReloadCounters verifies that only hot reloads count
// as successes (the initial startup load and no-op reloads do not) and that
// every failed attempt is counted, including log-deduplicated ones.
func TestCertReloaderMetricsReloadCounters(t *testing.T) {
	initTestMetrics(t)

	dir := t.TempDir()
	r, certPath, keyPath := newTestReloader(t, dir, 0)

	if body := scrapeMetrics(t); strings.Contains(body, "e2bgw_tls_certificate_reloads_total{") {
		t.Error("initial startup load must not increment the reload counter")
	}

	rotated := writeTestCert(t, certPath, keyPath)
	if err := r.reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	waitForSerial(t, r, rotated.SerialNumber)
	if body := scrapeMetrics(t); !strings.Contains(body, `e2bgw_tls_certificate_reloads_total{result="success"} 1`) {
		t.Errorf("expected one successful hot reload, got:\n%s", body)
	}

	// A no-op reload (unchanged files) is not counted as a hot reload.
	if err := r.reload(); err != nil {
		t.Fatalf("no-op reload: %v", err)
	}
	if body := scrapeMetrics(t); strings.Contains(body, `e2bgw_tls_certificate_reloads_total{result="success"} 2`) {
		t.Error("unchanged-file reload must not be counted")
	}

	// Every failed reload attempt is counted, even when the warning log is
	// deduplicated for identical failures.
	if err := os.WriteFile(certPath, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("writing invalid certificate: %v", err)
	}
	for range 2 {
		if err := r.reload(); err == nil {
			t.Fatal("expected reload to fail with an invalid certificate")
		}
	}
	if body := scrapeMetrics(t); !strings.Contains(body, `e2bgw_tls_certificate_reloads_total{result="failure"} 2`) {
		t.Errorf("expected two counted reload failures, got:\n%s", body)
	}
}

// TestCertReloaderMetricsExpiryTimestamp verifies that the expiry gauge
// follows the served certificate: the initial load sets it, a hot reload
// updates it, and a failed reload leaves the served certificate's expiry.
func TestCertReloaderMetricsExpiryTimestamp(t *testing.T) {
	initTestMetrics(t)

	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	now := time.Now()
	initial := tlstest.WriteCert(t, certPath, keyPath, now.Add(-time.Hour), now.Add(30*24*time.Hour))

	r, err := newCertReloader(certPath, keyPath, 0)
	if err != nil {
		t.Fatalf("newCertReloader: %v", err)
	}
	assertExpiryGauge(t, initial.NotAfter)

	rotated := tlstest.WriteCert(t, certPath, keyPath, now.Add(-time.Hour), now.Add(60*24*time.Hour))
	if err := r.reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	assertExpiryGauge(t, rotated.NotAfter)

	if err := os.WriteFile(certPath, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("writing invalid certificate: %v", err)
	}
	if err := r.reload(); err == nil {
		t.Fatal("expected reload to fail with an invalid certificate")
	}
	assertExpiryGauge(t, rotated.NotAfter)
}

// assertExpiryGauge fails the test unless the scraped certificate expiry
// gauge equals want as a Unix timestamp.
func assertExpiryGauge(t *testing.T, want time.Time) {
	t.Helper()

	const sample = "e2bgw_tls_certificate_expiry_timestamp_seconds "
	body := scrapeMetrics(t)
	for line := range strings.SplitSeq(body, "\n") {
		value, ok := strings.CutPrefix(line, sample)
		if !ok {
			continue
		}
		got, err := strconv.ParseFloat(value, 64)
		if err != nil {
			t.Fatalf("parsing expiry gauge value %q: %v", value, err)
		}
		if int64(got) != want.Unix() {
			t.Fatalf("expiry gauge = %d, want %d (%s)", int64(got), want.Unix(), want)
		}
		return
	}
	t.Fatalf("metrics output has no %q sample, got:\n%s", sample, body)
}
