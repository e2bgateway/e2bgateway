package envd

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Envelope flags
const (
	EnvelopeFlagNone       byte = 0x00
	EnvelopeFlagCompressed byte = 0x01
	EnvelopeFlagEndStream  byte = 0x02
)

// Envelope represents a ConnectRPC streaming envelope.
// Format: [flags:1byte][length:4bytes][payload:length bytes]
type Envelope struct {
	Flags   byte
	Payload []byte
}

// EncodeEnvelope encodes a message into a ConnectRPC envelope.
func EncodeEnvelope(flags byte, msg interface{}) ([]byte, error) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshaling envelope payload: %w", err)
	}

	// Header: flags (1 byte) + length (4 bytes, big-endian)
	header := make([]byte, 5, 5+len(payload))
	header[0] = flags
	binary.BigEndian.PutUint32(header[1:5], uint32(len(payload)))

	return append(header, payload...), nil
}

// DecodeEnvelope decodes a ConnectRPC envelope from a reader.
func DecodeEnvelope(r io.Reader) (*Envelope, error) {
	// Read header (5 bytes)
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, fmt.Errorf("reading envelope header: %w", err)
	}

	flags := header[0]
	length := binary.BigEndian.Uint32(header[1:5])

	// Read payload
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("reading envelope payload: %w", err)
	}

	return &Envelope{
		Flags:   flags,
		Payload: payload,
	}, nil
}

// DecodeEnvelopeFromBytes decodes a ConnectRPC envelope from bytes.
func DecodeEnvelopeFromBytes(data []byte) (*Envelope, error) {
	if len(data) < 5 {
		return nil, fmt.Errorf("envelope too short: %d bytes", len(data))
	}

	flags := data[0]
	length := binary.BigEndian.Uint32(data[1:5])

	if uint32(len(data)-5) < length {
		return nil, fmt.Errorf("envelope payload truncated: expected %d bytes, got %d", length, len(data)-5)
	}

	payload := make([]byte, length)
	copy(payload, data[5:5+length])

	return &Envelope{
		Flags:   flags,
		Payload: payload,
	}, nil
}

// UnmarshalPayload unmarshals the envelope payload into the given message.
func (e *Envelope) UnmarshalPayload(msg interface{}) error {
	return json.Unmarshal(e.Payload, msg)
}

// IsEndStream returns true if this envelope marks the end of a stream.
func (e *Envelope) IsEndStream() bool {
	return e.Flags&EnvelopeFlagEndStream != 0
}

// StreamTrailer represents the trailer message at the end of a ConnectRPC stream.
type StreamTrailer struct {
	Error    *StreamError        `json:"error,omitempty"`
	Metadata map[string][]string `json:"metadata,omitempty"`
}

// StreamError represents an error in a ConnectRPC stream.
type StreamError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ReadStream reads all envelopes from a streaming response.
// Calls the handler function for each envelope.
// Stops when it encounters an end-stream envelope or EOF.
func ReadStream(r io.Reader, handler func(*Envelope) error) error {
	for {
		env, err := DecodeEnvelope(r)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decoding envelope: %w", err)
		}

		if err := handler(env); err != nil {
			return err
		}

		if env.IsEndStream() {
			// Check for error in trailer
			var trailer StreamTrailer
			if err := env.UnmarshalPayload(&trailer); err == nil && trailer.Error != nil {
				return fmt.Errorf("stream error: %s: %s", trailer.Error.Code, trailer.Error.Message)
			}
			return nil
		}
	}
}
