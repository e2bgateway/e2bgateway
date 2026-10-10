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

package envd

import (
	"bytes"
	"strings"
	"testing"
)

func TestEncodeEnvelope(t *testing.T) {
	msg := map[string]string{"key": "value"}
	data, err := EncodeEnvelope(EnvelopeFlagNone, msg)
	if err != nil {
		t.Fatalf("failed to encode envelope: %v", err)
	}

	// Verify header
	if data[0] != EnvelopeFlagNone {
		t.Errorf("expected flags 0x00, got 0x%02x", data[0])
	}

	// Verify we can decode it
	env, err := DecodeEnvelopeFromBytes(data)
	if err != nil {
		t.Fatalf("failed to decode envelope: %v", err)
	}

	if env.Flags != EnvelopeFlagNone {
		t.Errorf("expected flags 0x00, got 0x%02x", env.Flags)
	}

	var decoded map[string]string
	if err := env.UnmarshalPayload(&decoded); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if decoded["key"] != "value" {
		t.Errorf("expected key=value, got %q", decoded["key"])
	}
}

func TestEncodeEnvelope_EndStream(t *testing.T) {
	trailer := StreamTrailer{
		Metadata: map[string][]string{"trailer": {"value"}},
	}

	data, err := EncodeEnvelope(EnvelopeFlagEndStream, trailer)
	if err != nil {
		t.Fatalf("failed to encode envelope: %v", err)
	}

	env, err := DecodeEnvelopeFromBytes(data)
	if err != nil {
		t.Fatalf("failed to decode envelope: %v", err)
	}

	if !env.IsEndStream() {
		t.Error("expected end-stream envelope")
	}

	var decoded StreamTrailer
	if err := env.UnmarshalPayload(&decoded); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if decoded.Metadata["trailer"][0] != "value" {
		t.Errorf("expected trailer=value, got %q", decoded.Metadata["trailer"][0])
	}
}

func TestDecodeEnvelopeFromBytes_TooShort(t *testing.T) {
	data := []byte{0x00, 0x00}

	_, err := DecodeEnvelopeFromBytes(data)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !containsString(err.Error(), "envelope too short") {
		t.Errorf("expected error to contain 'envelope too short', got %q", err.Error())
	}
}

func TestDecodeEnvelopeFromBytes_Truncated(t *testing.T) {
	// Create envelope with length 10 but only provide 5 bytes of payload
	data := []byte{0x00, 0x00, 0x00, 0x00, 0x0a, 0x01, 0x02, 0x03, 0x04, 0x05}

	_, err := DecodeEnvelopeFromBytes(data)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !containsString(err.Error(), "truncated") {
		t.Errorf("expected error to contain 'truncated', got %q", err.Error())
	}
}

func TestDecodeEnvelope_PayloadTooLarge(t *testing.T) {
	// Build a header with length = maxEnvelopePayload + 1.
	// The decoder must reject it without allocating the payload.
	header := make([]byte, 5)
	header[0] = EnvelopeFlagNone
	bigLen := uint32(maxEnvelopePayload) + 1
	header[1] = byte(bigLen >> 24)
	header[2] = byte(bigLen >> 16)
	header[3] = byte(bigLen >> 8)
	header[4] = byte(bigLen)

	_, err := DecodeEnvelope(bytes.NewReader(header))
	if err == nil {
		t.Fatal("expected error for oversized payload, got nil")
	}
	if !containsString(err.Error(), "too large") {
		t.Errorf("expected error to contain 'too large', got %q", err.Error())
	}
}

func TestDecodeEnvelopeFromBytes_PayloadTooLarge(t *testing.T) {
	// Build data buffer with oversized length header.
	data := make([]byte, 10)
	data[0] = EnvelopeFlagNone
	bigLen := uint32(maxEnvelopePayload) + 1
	data[1] = byte(bigLen >> 24)
	data[2] = byte(bigLen >> 16)
	data[3] = byte(bigLen >> 8)
	data[4] = byte(bigLen)

	_, err := DecodeEnvelopeFromBytes(data)
	if err == nil {
		t.Fatal("expected error for oversized payload, got nil")
	}
	if !containsString(err.Error(), "too large") {
		t.Errorf("expected error to contain 'too large', got %q", err.Error())
	}
}

func TestDecodeEnvelope_PayloadAtLimit(t *testing.T) {
	// Build a payload just under the limit (1 MiB string) to ensure legitimate
	// large envelopes still pass the size check.
	msg := map[string]string{"data": strings.Repeat("x", 1<<20)}
	data, err := EncodeEnvelope(EnvelopeFlagNone, msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	env, err := DecodeEnvelopeFromBytes(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Payload) <= 1<<20 {
		t.Errorf("expected payload > 1 MiB, got %d", len(env.Payload))
	}
}

func TestReadStream(t *testing.T) {
	// Create multiple envelopes
	env1, _ := EncodeEnvelope(EnvelopeFlagNone, map[string]string{"data": "1"})
	env2, _ := EncodeEnvelope(EnvelopeFlagNone, map[string]string{"data": "2"})
	env3, _ := EncodeEnvelope(EnvelopeFlagEndStream, StreamTrailer{})

	var buf bytes.Buffer
	buf.Write(env1)
	buf.Write(env2)
	buf.Write(env3)

	var count int
	err := ReadStream(&buf, func(env *Envelope) error {
		count++
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if count != 3 {
		t.Errorf("expected 3 envelopes, got %d", count)
	}
}

func TestReadStream_WithError(t *testing.T) {
	trailer := StreamTrailer{
		Error: &StreamError{
			Code:    "internal",
			Message: "test error",
		},
	}

	env, _ := EncodeEnvelope(EnvelopeFlagEndStream, trailer)

	err := ReadStream(bytes.NewReader(env), func(env *Envelope) error {
		return nil
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !containsString(err.Error(), "test error") {
		t.Errorf("expected error to contain 'test error', got %q", err.Error())
	}
}

func TestEnvelope_IsEndStream(t *testing.T) {
	tests := []struct {
		name     string
		flags    byte
		expected bool
	}{
		{"none", EnvelopeFlagNone, false},
		{"compressed", EnvelopeFlagCompressed, false},
		{"end-stream", EnvelopeFlagEndStream, true},
		{"combined", EnvelopeFlagCompressed | EnvelopeFlagEndStream, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := &Envelope{Flags: tt.flags}
			if env.IsEndStream() != tt.expected {
				t.Errorf("expected IsEndStream()=%v, got %v", tt.expected, env.IsEndStream())
			}
		})
	}
}
