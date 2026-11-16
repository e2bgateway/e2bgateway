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

package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/e2bgateway/e2bgateway/internal/api/dto"
	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/server"
	"github.com/e2bgateway/e2bgateway/test/tlstest"
)

// httpsGateway drives a running HTTPS gateway instance.
type httpsGateway struct {
	baseURL  string
	certPath string
	keyPath  string
	rootCAs  *x509.CertPool
	client   *http.Client
}

// startHTTPSGateway boots a real gateway server (mock backend) whose HTTPS
// listener terminates TLS with the given certificate pair, and returns a
// fixture with an HTTPS client trusting the certificate.
func startHTTPSGateway(t *testing.T, certPath, keyPath string, reloadInterval time.Duration) *httpsGateway {
	t.Helper()

	addr := tlstest.FreeAddr(t)

	cfg := &config.Config{
		Server: config.ServerConfig{
			HTTP:  config.HTTPConfig{Address: "127.0.0.1:0"},
			HTTPS: config.HTTPSConfig{Address: addr, CertFile: certPath, KeyFile: keyPath, ReloadInterval: reloadInterval},
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
	require.NoError(t, err, "creating server")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		require.NoError(t, srv.Stop(context.Background()), "stopping server")
	})

	go func() {
		_ = srv.Start(ctx)
	}()

	rootCAs := x509.NewCertPool()
	tlstest.TrustCertFile(t, rootCAs, certPath)

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    rootCAs,
				MinVersion: tls.VersionTLS12,
			},
		},
	}

	return &httpsGateway{
		baseURL:  "https://" + addr,
		certPath: certPath,
		keyPath:  keyPath,
		rootCAs:  rootCAs,
		client:   client,
	}
}

// waitUntilServing polls the gateway until an HTTPS request succeeds, so the
// listener startup does not race the first request.
func (g *httpsGateway) waitUntilServing(t *testing.T) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := g.client.Get(g.baseURL + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("HTTPS gateway did not become ready: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// doJSON performs a JSON request against the HTTPS gateway.
func (g *httpsGateway) doJSON(t *testing.T, method, path string, body any) *http.Response {
	t.Helper()

	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(t, err, "marshaling body")
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, g.baseURL+path, reqBody)
	require.NoError(t, err, "creating request")
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	require.NoError(t, err, "%s %s", method, path)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestHTTPS_APIOverTLS drives the E2B sandbox API end to end through the
// gateway's real HTTPS listener: the control plane must behave identically
// over TLS, including health endpoints and a full sandbox lifecycle.
func TestHTTPS_APIOverTLS(t *testing.T) {
	certPath, keyPath := tlstest.WriteValidCert(t, t.TempDir())
	gw := startHTTPSGateway(t, certPath, keyPath, 0)
	gw.waitUntilServing(t)

	// Health endpoints over TLS.
	healthResp := gw.doJSON(t, http.MethodGet, "/healthz", nil)
	require.Equal(t, http.StatusOK, healthResp.StatusCode)

	readyResp := gw.doJSON(t, http.MethodGet, "/readyz", nil)
	require.Equal(t, http.StatusOK, readyResp.StatusCode)

	// Sandbox lifecycle over TLS: create -> get -> kill.
	createResp := gw.doJSON(t, http.MethodPost, "/sandboxes", dto.SandboxCreateRequest{TemplateID: "base"})
	require.Equal(t, http.StatusCreated, createResp.StatusCode)
	var created dto.SandboxCreateResponse
	decodeJSON(t, createResp, &created)
	require.NotEmpty(t, created.SandboxID)

	getResp := gw.doJSON(t, http.MethodGet, "/sandboxes/"+created.SandboxID, nil)
	require.Equal(t, http.StatusOK, getResp.StatusCode)
	var info dto.SandboxInfo
	decodeJSON(t, getResp, &info)
	require.Equal(t, created.SandboxID, info.SandboxID)
	require.Equal(t, "running", info.State)

	killResp := gw.doJSON(t, http.MethodDelete, "/sandboxes/"+created.SandboxID, nil)
	require.Equal(t, http.StatusNoContent, killResp.StatusCode)
}

// TestHTTPS_CertRotation swaps the certificate files under a running gateway
// and verifies, over real TLS handshakes, that the new certificate is served
// without a restart.
func TestHTTPS_CertRotation(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := tlstest.WriteValidCert(t, dir)
	gw := startHTTPSGateway(t, certPath, keyPath, 50*time.Millisecond)
	gw.waitUntilServing(t)

	addr := gw.baseURL[len("https://"):]
	serialA, err := tlstest.PeerSerial(addr, gw.rootCAs)
	require.NoError(t, err, "TLS handshake")

	// Rotate: overwrite both files in place with a freshly generated pair.
	tlstest.WriteValidCert(t, dir)
	serialB := tlstest.SerialFromFile(t, certPath)
	require.NotEqual(t, serialA, serialB, "expected the rotated certificate to differ")

	tlstest.TrustCertFile(t, gw.rootCAs, certPath)
	tlstest.WaitForPeerSerial(t, addr, gw.rootCAs, serialB)

	// The API keeps serving over TLS after the rotation.
	healthResp := gw.doJSON(t, http.MethodGet, "/healthz", nil)
	require.Equal(t, http.StatusOK, healthResp.StatusCode)
}
