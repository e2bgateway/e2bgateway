package envd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// readEnvelopeRequest reads a ConnectRPC envelope from the request body
// and unmarshals the payload into the target. This is used by mock test
// servers to decode requests from the envd client (which sends envelopes).
// If the body is not envelope-framed, it falls back to plain JSON decode.
func readEnvelopeRequest(r *http.Request, target interface{}) error {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("reading body: %w", err)
	}
	if len(data) == 0 {
		return fmt.Errorf("empty body")
	}

	// Try envelope decode first (ConnectRPC format: 5-byte header + payload)
	if len(data) >= 5 {
		env, err := DecodeEnvelopeFromBytes(data)
		if err == nil {
			return json.Unmarshal(env.Payload, target)
		}
	}

	// Fallback: plain JSON
	return json.Unmarshal(data, target)
}

// writeEnvelopeResponse writes a ConnectRPC envelope response.
// If the client expects envelope-framed responses (ConnectRPC format),
// this wraps the JSON payload in a 5-byte envelope header.
func writeEnvelopeResponse(w http.ResponseWriter, payload interface{}) {
	env, err := EncodeEnvelope(EnvelopeFlagNone, payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/connect+json")
	_, _ = w.Write(env)
}
