// Package api defines the public E2B API types for the gateway.
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
