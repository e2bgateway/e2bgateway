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

package api

// These constants define the API version and protocol supported by E2BGateway.
const (
	// APIVersion is the E2B API version implemented by this gateway.
	APIVersion = "v1"

	// APIPrefix is the URL prefix for all E2B API endpoints.
	APIPrefix = "/api/v1"

	// ProtocolVersion is the wire protocol version.
	ProtocolVersion = "1.0"
)

// Content types.
const (
	ContentTypeJSON      = "application/json"
	ContentTypeOctet     = "application/octet-stream"
	ContentTypeMultipart = "multipart/form-data"
)

// Header names.
const (
	HeaderAPIKey        = "X-API-Key"
	HeaderAuthorization = "Authorization"
	HeaderRequestID     = "X-Request-Id"
	HeaderTraceID       = "X-Trace-Id"
)
