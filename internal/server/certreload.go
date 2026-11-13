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

	// lastFailureKey deduplicates repeated identical reload failures (e.g. a
	// cert-manager rotation window gone wrong leaves the files unreadable or
	// invalid for many ticks): at most one warning is emitted per distinct
	// failure until the files load successfully or the failing content changes.
	lastFailureKey string
}

// newCertReloader loads the certificate pair once and fails fast when it is unusable.
func newCertReloader(certFile, keyFile string, interval time.Duration) (*certReloader, error) {
	r := &certReloader{
		certFile: certFile,
		keyFile:  keyFile,
		interval: interval,
	}

	if err := r.reload(); err != nil {
		return nil, err
	}
	return r, nil
}

// GetCertificate returns the currently loaded certificate for TLS handshakes.
// ClientHelloInfo is intentionally ignored: the gateway serves a single
// certificate for all connections and does not do SNI-based selection.
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
			_ = r.reload()
			// Re-check expiry on every tick: a certificate that never
			// changes on disk can still cross a warning threshold or expire.
			r.checkExpiry()
		}
	}
}

// certExpiryWarnWindow is how close to expiry a certificate must be before a warning is logged.
const certExpiryWarnWindow = 7 * 24 * time.Hour

// load parses the certificate pair from the already-read PEM buffers and its
// leaf certificate. A successful load resets the expiry-warning dedup state
// and re-arms it based on the new certificate.
func (r *certReloader) load(certPEM, keyPEM []byte) (*tls.Certificate, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parsing TLS certificate: %w", err)
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
// Both files are read exactly once per call: the change-detection digests and
// the served certificate are computed from the same bytes, so a rotation
// landing mid-check can never pair a digest from one file version with a
// certificate parsed from another. When the files are unchanged since the
// last successful load, reload is a no-op.
func (r *certReloader) reload() error {
	certPEM, keyPEM, err := readCertFiles(r.certFile, r.keyFile)
	if err != nil {
		r.logLoadFailure("read:"+err.Error(), err)
		return err
	}

	certDigest, keyDigest := sha256.Sum256(certPEM), sha256.Sum256(keyPEM)

	// Digest before parsing: if the parse fails the digests must stay
	// untouched so the next tick retries, and if it succeeds the digests
	// describe the bytes that were actually loaded and served.
	if !r.digestsChanged(certDigest, keyDigest) {
		return nil
	}

	cert, err := r.load(certPEM, keyPEM)
	if err != nil {
		r.logLoadFailure(fmt.Sprintf("content:%x:%x", certDigest, keyDigest), err)
		return err
	}

	r.current.Store(cert)
	r.recordDigests(certDigest, keyDigest)
	r.clearLoadFailure()

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

// digestsChanged reports whether the given content digests differ from the
// ones recorded at the last successful load. Digests detect any content
// change regardless of mtime preservation (cp -p, tar extraction) or
// filesystem mtime granularity.
func (r *certReloader) digestsChanged(certDigest, keyDigest [32]byte) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return certDigest != r.certDigest || keyDigest != r.keyDigest
}

// readCertFiles reads the certificate and key files exactly once per reload,
// so change detection and certificate parsing always observe the same bytes.
func readCertFiles(certFile, keyFile string) ([]byte, []byte, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, nil, fmt.Errorf("reading certificate file: %w", err)
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("reading key file: %w", err)
	}
	return certPEM, keyPEM, nil
}

// logLoadFailure warns about a failed reload at most once per distinct
// failure: identical failures (same unreadable-file error, or the same
// failing file contents) are deduplicated until the files load successfully
// or the failing content changes, mirroring the expiry-warning dedup.
func (r *certReloader) logLoadFailure(key string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if key == r.lastFailureKey {
		return
	}
	r.lastFailureKey = key
	slog.Warn("TLS certificate reload failed; keeping previous certificate",
		"certFile", r.certFile, "error", err)
}

// clearLoadFailure re-arms the reload-failure warning dedup after a
// successful load, so a later recurrence of the same failure warns again.
func (r *certReloader) clearLoadFailure() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastFailureKey = ""
}

// recordDigests records the certificate file content digests used for change detection.
func (r *certReloader) recordDigests(certDigest, keyDigest [32]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.certDigest = certDigest
	r.keyDigest = keyDigest
}
