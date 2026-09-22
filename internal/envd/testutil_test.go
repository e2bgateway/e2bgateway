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

// readJSONRequest reads a plain JSON request body (Connect unary format).
func readJSONRequest(r *http.Request, target interface{}) error {
	return json.NewDecoder(r.Body).Decode(target)
}

// writeJSONResponse writes a plain JSON response (Connect unary format).
// Used for unary RPC mock servers.
func writeJSONResponse(w http.ResponseWriter, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
