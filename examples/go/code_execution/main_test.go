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

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestRunStream_CanceledAfterHandshake(t *testing.T) {
	// The server accepts the execution request but never sends an execution result.
	requestRead := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		close(requestRead)
		<-release
	}))
	defer server.Close()
	defer close(release)
	t.Setenv("E2B_API_URL", server.URL)
	t.Setenv("E2B_API_KEY", "test-key")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- runStream(ctx, "sandbox-1") }()
	select {
	case <-requestRead:
	case err := <-result:
		t.Fatalf("stream ended before reading request: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive the execution request")
	}

	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not unblock the stream")
	}
}
