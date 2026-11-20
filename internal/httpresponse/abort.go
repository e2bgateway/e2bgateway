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

// Package httpresponse provides shared HTTP response handling.
package httpresponse

import (
	"log/slog"
	"net/http"
	"time"
)

// Abort interrupts a response without panicking or appending an error to a
// partially written body. A past write deadline prevents HTTP/1 final flushing
// and immediately resets an HTTP/2 stream. ResponseWriter wrappers must expose
// Unwrap so ResponseController can reach the server's writer. The server must
// support SetWriteDeadline, as the standard net/http HTTP/1 and HTTP/2 servers do.
func Abort(w http.ResponseWriter) {
	for current := w; current != nil; {
		if marker, ok := current.(interface{ MarkAborted() }); ok {
			marker.MarkAborted()
		}
		if wrapper, ok := current.(interface{ Unwrap() http.ResponseWriter }); ok {
			current = wrapper.Unwrap()
		} else {
			break
		}
	}
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(-time.Second)); err != nil {
		slog.Error("failed to interrupt HTTP response", "error", err)
	}
}
