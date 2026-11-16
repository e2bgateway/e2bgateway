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
	"net"
	"sync"
	"testing"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/test/tlstest"
)

// waitForListenerClosed polls until a TCP connection to addr fails, failing
// the test if addr still accepts connections after 5 seconds.
func waitForListenerClosed(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatalf("listener at %s still accepts connections", addr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestServerStopShutsDownListenersBeforeWaitingForWatcher verifies that a
// certificate reload still in progress when Stop is called does not keep the
// listeners accepting connections, and that Stop returns only once the
// reload has finished.
func TestServerStopShutsDownListenersBeforeWaitingForWatcher(t *testing.T) {
	certPath, keyPath := tlstest.WriteValidCert(t, t.TempDir())
	httpAddr, httpsAddr := tlstest.FreeAddr(t), tlstest.FreeAddr(t)

	srv, err := New(&config.Config{
		Server: config.ServerConfig{
			HTTP: config.HTTPConfig{Address: httpAddr},
			HTTPS: config.HTTPSConfig{
				Address:        httpsAddr,
				CertFile:       certPath,
				KeyFile:        keyPath,
				ReloadInterval: 10 * time.Millisecond,
			},
		},
		Backends: []config.BackendConfig{{Name: "mock", Type: "mock", Enabled: true}},
		Routing:  config.RoutingConfig{DefaultBackend: "mock", Strategy: "static"},
	})
	if err != nil {
		t.Fatalf("creating server: %v", err)
	}

	// Block the watcher's first reload until the test releases it. The hook
	// is installed before Start launches the watcher goroutine.
	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	releaseReload := func() { releaseOnce.Do(func() { close(release) }) }
	srv.httpsReloader.readFiles = func(certFile, keyFile string) ([]byte, []byte, error) {
		enterOnce.Do(func() { close(entered) })
		<-release
		return readCertFiles(certFile, keyFile)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = srv.Stop(context.Background())
	})
	// Cleanups run last-in first-out: a failing assertion unblocks the reload
	// before the cleanup above stops the server.
	t.Cleanup(releaseReload)
	go func() { _ = srv.Start(ctx) }()

	tlstest.WaitForListener(t, httpAddr)
	tlstest.WaitForListener(t, httpsAddr)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the certificate watcher did not start a reload")
	}

	stopErr := make(chan error, 1)
	go func() { stopErr <- srv.Stop(context.Background()) }()

	waitForListenerClosed(t, httpsAddr)
	waitForListenerClosed(t, httpAddr)
	select {
	case err := <-stopErr:
		t.Fatalf("Stop returned (%v) while a certificate reload was in progress", err)
	case <-time.After(100 * time.Millisecond):
	}

	releaseReload()
	select {
	case err := <-stopErr:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after the certificate reload finished")
	}
}
