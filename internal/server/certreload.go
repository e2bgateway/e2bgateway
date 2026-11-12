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

	mu          sync.Mutex
	certModTime time.Time
	certSize    int64
	keyModTime  time.Time
	keySize     int64
}

// newCertReloader loads the certificate pair once and fails fast when it is unusable.
func newCertReloader(certFile, keyFile string, interval time.Duration) (*certReloader, error) {
	r := &certReloader{
		certFile: certFile,
		keyFile:  keyFile,
		interval: interval,
	}

	cert, err := r.load()
	if err != nil {
		return nil, err
	}
	r.current.Store(cert)

	if err := r.updateSnapshot(); err != nil {
		return nil, err
	}
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
		}
	}
}

// certExpiryWarnWindow is how close to expiry a certificate must be before a warning is logged.
const certExpiryWarnWindow = 7 * 24 * time.Hour

// load reads and parses the certificate pair and its leaf certificate.
func (r *certReloader) load() (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading TLS certificate: %w", err)
	}
	if len(cert.Certificate) > 0 {
		if leaf, parseErr := x509.ParseCertificate(cert.Certificate[0]); parseErr == nil {
			cert.Leaf = leaf
			logCertExpiryStatus(r.certFile, leaf)
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
	cert, err := r.load()
	if err != nil {
		slog.Warn("TLS certificate reload failed; keeping previous certificate",
			"certFile", r.certFile, "error", err)
		return err
	}

	r.current.Store(cert)
	if snapshotErr := r.updateSnapshot(); snapshotErr != nil {
		slog.Warn("TLS certificate reloaded but file snapshot update failed",
			"certFile", r.certFile, "error", snapshotErr)
	}

	attrs := []any{"certFile", r.certFile}
	if cert.Leaf != nil {
		attrs = append(attrs, "notAfter", cert.Leaf.NotAfter)
	}
	slog.Info("TLS certificate reloaded", attrs...)
	return nil
}

// filesChanged reports whether either certificate file changed since the last load.
func (r *certReloader) filesChanged() bool {
	certInfo, certErr := os.Stat(r.certFile)
	keyInfo, keyErr := os.Stat(r.keyFile)
	if certErr != nil || keyErr != nil {
		return true
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	return !certInfo.ModTime().Equal(r.certModTime) ||
		certInfo.Size() != r.certSize ||
		!keyInfo.ModTime().Equal(r.keyModTime) ||
		keyInfo.Size() != r.keySize
}

// updateSnapshot records the certificate file metadata used for change detection.
func (r *certReloader) updateSnapshot() error {
	certInfo, err := os.Stat(r.certFile)
	if err != nil {
		return fmt.Errorf("stating certificate file: %w", err)
	}
	keyInfo, err := os.Stat(r.keyFile)
	if err != nil {
		return fmt.Errorf("stating key file: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.certModTime = certInfo.ModTime()
	r.certSize = certInfo.Size()
	r.keyModTime = keyInfo.ModTime()
	r.keySize = keyInfo.Size()
	return nil
}
