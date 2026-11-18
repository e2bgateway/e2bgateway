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

	"github.com/e2bgateway/e2bgateway/internal/metrics"
)

// certReloader serves a TLS certificate and reloads it when the backing files change.
type certReloader struct {
	certFile string
	keyFile  string
	interval time.Duration
	current  atomic.Pointer[tls.Certificate]

	// readFiles reads the certificate and key files. Tests replace it to
	// simulate file states that are hard to produce on disk deterministically.
	readFiles func(certFile, keyFile string) ([]byte, []byte, error)

	// certDigest and keyDigest are SHA-256 content digests of the certificate
	// and key files as of the last successful load. Comparing content digests
	// detects any file change, including rotations that preserve mtime
	// (cp -p, tar extraction) and writes within filesystem mtime granularity.
	mu         sync.Mutex
	certDigest [32]byte
	keyDigest  [32]byte

	// warnedStatus deduplicates expiry warnings on the watch ticks. It holds
	// the served certificate's expiry status as of the last successful load
	// or the last warning, and a tick warns only when the status changes, so
	// each threshold is reported once until the certificate is replaced.
	warnedStatus expiryStatus

	// lastFailureKey deduplicates repeated identical reload failures (e.g. a
	// cert-manager rotation window gone wrong leaves the files unreadable or
	// invalid for many ticks): at most one warning is emitted per distinct
	// failure until the files load successfully or the failing content changes.
	lastFailureKey string
}

// newCertReloader loads the certificate pair once and fails fast when it is unusable.
func newCertReloader(certFile, keyFile string, interval time.Duration) (*certReloader, error) {
	r := &certReloader{
		certFile:  certFile,
		keyFile:   keyFile,
		interval:  interval,
		readFiles: readCertFiles,
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
			// select picks at random among ready cases, so a tick can win
			// over a cancellation that arrived first: never start a reload
			// once the watcher is stopped.
			if ctx.Err() != nil {
				return
			}
			_ = r.reload()
			// Re-check expiry on every tick: a certificate that never
			// changes on disk can still cross a warning threshold or expire.
			r.checkExpiry()
		}
	}
}

// certExpiryWarnWindow is how close to expiry a certificate must be before a warning is logged.
const certExpiryWarnWindow = 7 * 24 * time.Hour

// expiryStatus classifies the current time against a certificate's validity
// window.
type expiryStatus int

const (
	// expiryOK means the certificate is valid and not near expiry.
	expiryOK expiryStatus = iota
	// expiryNearExpiry means the certificate expires within certExpiryWarnWindow.
	expiryNearExpiry
	// expiryExpired means the certificate's NotAfter has passed.
	expiryExpired
	// expiryNotYetValid means the certificate's NotBefore is in the future.
	expiryNotYetValid
)

// classifyExpiry reports the expiry status of leaf at now. Expiry is checked
// first, so a certificate with an inverted validity window (NotBefore after
// NotAfter) is reported as expired once its NotAfter has passed.
func classifyExpiry(leaf *x509.Certificate, now time.Time) expiryStatus {
	switch {
	case now.After(leaf.NotAfter):
		return expiryExpired
	case now.Before(leaf.NotBefore):
		return expiryNotYetValid
	case now.After(leaf.NotAfter.Add(-certExpiryWarnWindow)):
		return expiryNearExpiry
	default:
		return expiryOK
	}
}

// certPair is the content of the certificate and key files as read together
// by one load attempt, with the SHA-256 digests used for change detection.
type certPair struct {
	certPEM, keyPEM       []byte
	certDigest, keyDigest [32]byte
}

// sameContent reports whether p and other hold identical file contents.
func (p certPair) sameContent(other certPair) bool {
	return p.certDigest == other.certDigest && p.keyDigest == other.keyDigest
}

// readPair reads both files once and digests their content, so change
// detection and certificate parsing always observe the same bytes.
func (r *certReloader) readPair() (certPair, error) {
	certPEM, keyPEM, err := r.readFiles(r.certFile, r.keyFile)
	if err != nil {
		return certPair{}, err
	}
	return certPair{
		certPEM:    certPEM,
		keyPEM:     keyPEM,
		certDigest: sha256.Sum256(certPEM),
		keyDigest:  sha256.Sum256(keyPEM),
	}, nil
}

// reload swaps in a newly loaded certificate, keeping the old one on failure.
// Each attempt reads both files exactly once: the change-detection digests
// and the served certificate are computed from the same bytes, so a rotation
// landing mid-check can never pair a digest from one file version with a
// certificate parsed from another. When the files are unchanged since the
// last successful load, reload is a no-op.
//
// A pair that fails to parse is read once more before the failure is
// reported: tools that replace the certificate and key as two separate
// writes leave a brief window in which the new certificate sits next to the
// old key, and the immediate re-read usually observes the completed pair. A
// re-read that itself fails is reported like any other read failure instead
// of the stale parse error from the first read.
//
// reload is not safe for concurrent use: newCertReloader runs the initial
// load, and only the watch goroutine runs later ones.
func (r *certReloader) reload() error {
	pair, err := r.readPair()
	if err != nil {
		r.logLoadFailure("read:"+err.Error(), err)
		return err
	}
	if err = r.loadPair(pair); err == nil {
		return nil
	}
	retry, readErr := r.readPair()
	if readErr != nil {
		r.logLoadFailure("read:"+readErr.Error(), readErr)
		return readErr
	}
	if !retry.sameContent(pair) {
		if err = r.loadPair(retry); err == nil {
			return nil
		}
		pair = retry
	}
	r.logLoadFailure(fmt.Sprintf("content:%x:%x", pair.certDigest, pair.keyDigest), err)
	return err
}

// loadPair serves the certificate parsed from pair, or returns the parse
// error. Digests are recorded only after a successful parse: a pair that
// fails to parse is retried on the next tick, and the digests always
// describe the bytes actually served. A pair matching the last successful
// load is a no-op.
func (r *certReloader) loadPair(pair certPair) error {
	if !r.digestsChanged(pair.certDigest, pair.keyDigest) {
		// The files read successfully and match the last successful load:
		// any earlier failure has recovered, so re-arm failure warnings.
		r.clearLoadFailure()
		return nil
	}

	cert, err := parseKeyPair(pair.certPEM, pair.keyPEM)
	if err != nil {
		return err
	}
	// A nil previous certificate means this is the initial startup load,
	// which is not a hot reload and is not counted as one.
	initialLoad := r.current.Swap(cert) == nil
	r.recordDigests(pair.certDigest, pair.keyDigest)
	r.clearLoadFailure()

	attrs := []any{"certFile", r.certFile}
	if cert.Leaf != nil {
		attrs = append(attrs, "notAfter", cert.Leaf.NotAfter)
	}
	if initialLoad {
		slog.Info("TLS certificate loaded", attrs...)
	} else {
		metrics.RecordTLSReload("success")
		slog.Info("TLS certificate reloaded", attrs...)
	}
	if cert.Leaf != nil {
		metrics.SetTLSCertificateExpiry(cert.Leaf.NotAfter)
		r.rearmExpiryWarnings(cert.Leaf)
	}
	return nil
}

// publishExpiry exports the expiry gauge for the currently served
// certificate. Metrics helpers are no-ops before metrics.Init, so the
// startup load cannot do it itself; New calls this right after Init so the
// gauge exists before the first rotation. A certificate without a parsed
// leaf leaves the gauge unset, matching loadPair.
func (r *certReloader) publishExpiry() {
	cert := r.current.Load()
	if cert == nil || cert.Leaf == nil {
		return
	}
	metrics.SetTLSCertificateExpiry(cert.Leaf.NotAfter)
}

// parseKeyPair parses a PEM certificate chain and its private key, making
// sure the result carries the parsed leaf certificate.
func parseKeyPair(certPEM, keyPEM []byte) (*tls.Certificate, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parsing TLS certificate: %w", err)
	}
	// X509KeyPair populates Leaf itself unless GODEBUG=x509keypairleaf=0.
	if cert.Leaf == nil && len(cert.Certificate) > 0 {
		leaf, parseErr := x509.ParseCertificate(cert.Certificate[0])
		if parseErr != nil {
			slog.Warn("certificate leaf parse fallback failed; expiry gauge and warnings unavailable for this certificate",
				"error", parseErr)
		} else {
			cert.Leaf = leaf
		}
	}
	return &cert, nil
}

// checkExpiry re-evaluates the expiry status of the currently served
// certificate, warning once per status change until the certificate is
// replaced by a successful load.
func (r *certReloader) checkExpiry() {
	cert := r.current.Load()
	if cert == nil || cert.Leaf == nil {
		return
	}
	status := classifyExpiry(cert.Leaf, time.Now())
	if status != expiryOK && r.markExpiryWarned(status) {
		r.logExpiryWarning(status, cert.Leaf)
	}
}

// markExpiryWarned records that status was warned about and reports whether
// it differs from the status recorded by the last warning or successful load.
func (r *certReloader) markExpiryWarned(status expiryStatus) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if status == r.warnedStatus {
		return false
	}
	r.warnedStatus = status
	return true
}

// rearmExpiryWarnings resets the expiry-warning dedup state for a newly
// loaded certificate and warns right away when it is expired, not yet
// valid, or near expiry. The status is classified once and used for both
// the warning and the dedup state, so the two cannot disagree about a
// threshold crossed between separate clock reads.
func (r *certReloader) rearmExpiryWarnings(leaf *x509.Certificate) {
	status := classifyExpiry(leaf, time.Now())

	r.mu.Lock()
	r.warnedStatus = status
	r.mu.Unlock()

	r.logExpiryWarning(status, leaf)
}

// logExpiryWarning warns about an expired, not yet valid, or near-expiry
// certificate. Serving continues regardless: the certificate files are the
// source of truth.
func (r *certReloader) logExpiryWarning(status expiryStatus, leaf *x509.Certificate) {
	switch status {
	case expiryExpired:
		slog.Warn("TLS certificate has expired and will be served until replaced",
			"certFile", r.certFile, "notAfter", leaf.NotAfter, "serial", leaf.SerialNumber)
	case expiryNotYetValid:
		slog.Warn("TLS certificate is not yet valid and will be served anyway",
			"certFile", r.certFile, "notBefore", leaf.NotBefore, "serial", leaf.SerialNumber)
	case expiryNearExpiry:
		slog.Warn("TLS certificate expires soon",
			"certFile", r.certFile, "notAfter", leaf.NotAfter, "serial", leaf.SerialNumber)
	case expiryOK:
		// Nothing to warn about.
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

// readCertFiles reads the certificate and key files from disk; it is the
// readFiles implementation outside of tests.
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
// or the failing content changes, mirroring the expiry-warning dedup. The
// reload metric counts every failed hot reload regardless of log dedup.
//
// A failed initial load is neither logged nor counted: there is no previous
// certificate to keep, and newCertReloader returns the error, which aborts
// startup.
func (r *certReloader) logLoadFailure(key string, err error) {
	if r.current.Load() == nil {
		return
	}
	metrics.RecordTLSReload("failure")
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
