// Package protocol defines the canonical messages, events, and stream frames.
// Codecs validate wire data at the boundary; runtime ownership and scheduling
// remain outside these serializable contracts.
package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

func EncodeSessionEvent(event SessionEvent) ([]byte, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}

	return json.Marshal(event)
}

func DecodeSessionEvent(data []byte) (SessionEvent, error) {
	var event SessionEvent
	if err := decodeOne(data, &event); err != nil {
		return SessionEvent{}, err
	}
	if err := event.Validate(); err != nil {
		return SessionEvent{}, err
	}

	return event, nil
}

func EncodeStreamChunk(chunk StreamChunk) ([]byte, error) {
	if err := chunk.Validate(); err != nil {
		return nil, err
	}

	return json.Marshal(chunk)
}

func DecodeStreamChunk(data []byte) (StreamChunk, error) {
	var chunk StreamChunk
	if err := decodeOne(data, &chunk); err != nil {
		return StreamChunk{}, err
	}
	if err := chunk.Validate(); err != nil {
		return StreamChunk{}, err
	}

	return chunk, nil
}

// decodeOne accepts exactly one JSON value. Concrete types apply their own
// field validation; this helper also rejects trailing values or malformed input.
func decodeOne(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return err
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: trailing JSON value", ErrInvalidProtocol)
		}

		return err
	}

	return nil
}

func EncodeSessionHeader(header SessionHeader) ([]byte, error) {
	if err := header.Validate(); err != nil {
		return nil, err
	}

	return json.Marshal(header)
}

func DecodeSessionHeader(data []byte) (SessionHeader, error) {
	var header SessionHeader
	err := decodeOne(data, &header)
	return header, err
}
