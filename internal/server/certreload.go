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
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// certReloader serves a TLS certificate and reloads it when the backing files change.
type certReloader struct {
	certFile string
	keyFile  string
	interval time.Duration
	current  atomic.Pointer[tls.Certificate]

	// certDigest and keyDigest are SHA-256 content digests of the certificate
	// and key files as of the last successful load. Comparing content digests
	// detects any file change, including rotations that preserve mtime
	// (cp -p, tar extraction) and writes within filesystem mtime granularity.
	mu         sync.Mutex
	certDigest [32]byte
	keyDigest  [32]byte

	// warnedNearExpiry and warnedExpired deduplicate expiry warnings on the
	// watch ticks: at most one warning is emitted per threshold until the
	// certificate is replaced by a successful load.
	warnedNearExpiry bool
	warnedExpired    bool
}

// newCertReloader loads the certificate pair once and fails fast when it is unusable.
func newCertReloader(certFile, keyFile string, interval time.Duration) (*certReloader, error) {
	r := &certReloader{
		certFile: certFile,
		keyFile:  keyFile,
		interval: interval,
	}

	certDigest, keyDigest, err := r.readDigests()
	if err != nil {
		return nil, err
	}
	cert, err := r.load()
	if err != nil {
		return nil, err
	}
	r.current.Store(cert)
	r.recordDigests(certDigest, keyDigest)
	return r, nil
}

// GetCertificate returns the currently loaded certificate for TLS handshakes.
func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	cert := r.current.Load()
	if cert == nil {
		return nil, fmt.Errorf("no TLS certificate loaded")
	}
	return cert, nil
}

// watch polls the certificate files until ctx is cancelled.
func (r *certReloader) watch(ctx context.Context) {
	if r.interval <= 0 {
		return
	}

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if r.filesChanged() {
				_ = r.reload()
			}
			// Re-check expiry on every tick: a certificate that never
			// changes on disk can still cross a warning threshold or expire.
			r.checkExpiry()
		}
	}
}

// certExpiryWarnWindow is how close to expiry a certificate must be before a warning is logged.
const certExpiryWarnWindow = 7 * 24 * time.Hour

// load reads and parses the certificate pair and its leaf certificate.
// A successful load resets the expiry-warning dedup state and re-arms it
// based on the new certificate.
func (r *certReloader) load() (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading TLS certificate: %w", err)
	}
	if len(cert.Certificate) > 0 {
		if leaf, parseErr := x509.ParseCertificate(cert.Certificate[0]); parseErr == nil {
			cert.Leaf = leaf
			logCertExpiryStatus(r.certFile, leaf)
			r.rearmExpiryWarnings(leaf)
		}
	}
	return &cert, nil
}

// logCertExpiryStatus warns when a freshly loaded certificate is expired or near
// expiry. Serving continues regardless: the certificate files are the source of truth.
func logCertExpiryStatus(certFile string, leaf *x509.Certificate) {
	now := time.Now()
	switch {
	case now.After(leaf.NotAfter):
		slog.Warn("TLS certificate has expired and will be served until replaced",
			"certFile", certFile, "notAfter", leaf.NotAfter, "serial", leaf.SerialNumber)
	case now.After(leaf.NotAfter.Add(-certExpiryWarnWindow)):
		slog.Warn("TLS certificate expires soon",
			"certFile", certFile, "notAfter", leaf.NotAfter, "serial", leaf.SerialNumber)
	}
}

// reload swaps in a newly loaded certificate, keeping the old one on failure.
func (r *certReloader) reload() error {
	// Digest before loading: if the load fails the digests must stay
	// untouched so the next tick retries, and if it succeeds the digests
	// must describe the files that were actually loaded.
	certDigest, keyDigest, err := r.readDigests()
	if err != nil {
		slog.Warn("TLS certificate reload failed; keeping previous certificate",
			"certFile", r.certFile, "error", err)
		return err
	}

	cert, err := r.load()
	if err != nil {
		slog.Warn("TLS certificate reload failed; keeping previous certificate",
			"certFile", r.certFile, "error", err)
		return err
	}

	r.current.Store(cert)
	r.recordDigests(certDigest, keyDigest)

	attrs := []any{"certFile", r.certFile}
	if cert.Leaf != nil {
		attrs = append(attrs, "notAfter", cert.Leaf.NotAfter)
	}
	slog.Info("TLS certificate reloaded", attrs...)
	return nil
}

// checkExpiry re-evaluates the expiry status of the currently served
// certificate, warning at most once per threshold until the certificate is
// replaced by a successful load.
func (r *certReloader) checkExpiry() {
	cert := r.current.Load()
	if cert == nil || cert.Leaf == nil {
		return
	}
	leaf := cert.Leaf
	now := time.Now()

	switch {
	case now.After(leaf.NotAfter):
		if r.markExpiryWarned(&r.warnedExpired) {
			slog.Warn("TLS certificate has expired and will be served until replaced",
				"certFile", r.certFile, "notAfter", leaf.NotAfter, "serial", leaf.SerialNumber)
		}
	case now.After(leaf.NotAfter.Add(-certExpiryWarnWindow)):
		if r.markExpiryWarned(&r.warnedNearExpiry) {
			slog.Warn("TLS certificate expires soon",
				"certFile", r.certFile, "notAfter", leaf.NotAfter, "serial", leaf.SerialNumber)
		}
	}
}

// markExpiryWarned records that a warning was emitted for a threshold and
// reports whether this call was the first one since the last successful load.
func (r *certReloader) markExpiryWarned(flag *bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if *flag {
		return false
	}
	*flag = true
	return true
}

// rearmExpiryWarnings resets the per-threshold warning dedup state after a
// successful load and immediately marks the thresholds triggered by the new
// certificate, so tick re-checks do not duplicate load's own warning.
func (r *certReloader) rearmExpiryWarnings(leaf *x509.Certificate) {
	now := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()
	r.warnedExpired = false
	r.warnedNearExpiry = false
	switch {
	case now.After(leaf.NotAfter):
		r.warnedExpired = true
	case now.After(leaf.NotAfter.Add(-certExpiryWarnWindow)):
		r.warnedNearExpiry = true
	}
}

// filesChanged reports whether either certificate file changed since the last
// load by comparing SHA-256 content digests. Digests detect any content change
// regardless of mtime preservation (cp -p, tar extraction) or filesystem mtime
// granularity. Read errors (missing or unreadable files) count as "changed" so
// the next reload attempt surfaces the problem.
func (r *certReloader) filesChanged() bool {
	certDigest, keyDigest, err := r.readDigests()
	if err != nil {
		return true
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	return certDigest != r.certDigest || keyDigest != r.keyDigest
}

// readDigests reads the certificate and key files and returns their SHA-256
// content digests.
func (r *certReloader) readDigests() ([32]byte, [32]byte, error) {
	certPEM, err := os.ReadFile(r.certFile)
	if err != nil {
		return [32]byte{}, [32]byte{}, fmt.Errorf("reading certificate file: %w", err)
	}
	keyPEM, err := os.ReadFile(r.keyFile)
	if err != nil {
		return [32]byte{}, [32]byte{}, fmt.Errorf("reading key file: %w", err)
	}
	return sha256.Sum256(certPEM), sha256.Sum256(keyPEM), nil
}

// recordDigests records the certificate file content digests used for change detection.
func (r *certReloader) recordDigests(certDigest, keyDigest [32]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.certDigest = certDigest
	r.keyDigest = keyDigest
}
