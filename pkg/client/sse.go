package client

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// SSEEvent represents a single Server-Sent Event.
type SSEEvent struct {
	Type string // "event" field value (empty for default "message" events)
	Data string // "data" field value (concatenated if multi-line)
	ID   string // "id" field value
}

// SSEStream reads Server-Sent Events from an io.ReadCloser.
type SSEStream struct {
	reader  io.ReadCloser
	scanner *bufio.Scanner
	done    bool
}

// NewSSEStream creates a stream reader from an HTTP response body.
func NewSSEStream(body io.ReadCloser) *SSEStream {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	return &SSEStream{
		reader:  body,
		scanner: scanner,
	}
}

// Next reads the next SSE event from the stream.
// Returns io.EOF when the stream is closed.
func (s *SSEStream) Next() (*SSEEvent, error) {
	if s.done {
		return nil, io.EOF
	}

	var event SSEEvent
	var dataLines []string
	hasData := false

	for s.scanner.Scan() {
		line := s.scanner.Text()

		// Empty line = end of event
		if line == "" {
			if hasData {
				event.Data = strings.Join(dataLines, "\n")
				return &event, nil
			}
			// Empty event (no data), keep reading
			continue
		}

		// Parse field: value
		if strings.HasPrefix(line, ":") {
			// Comment line, ignore
			continue
		}

		field, value, _ := strings.Cut(line, ":")
		// Strip single leading space from value per SSE spec
		value = strings.TrimPrefix(value, " ")

		switch field {
		case "event":
			event.Type = value
		case "data":
			dataLines = append(dataLines, value)
			hasData = true
		case "id":
			event.ID = value
		case "retry":
			// Ignore retry field
		}
	}

	s.done = true

	if err := s.scanner.Err(); err != nil {
		return nil, fmt.Errorf("SSE stream read error: %w", err)
	}

	// If we have buffered data that wasn't terminated by blank line
	if hasData {
		event.Data = strings.Join(dataLines, "\n")
		return &event, nil
	}

	return nil, io.EOF
}

// Close closes the underlying reader.
func (s *SSEStream) Close() error {
	s.done = true
	return s.reader.Close()
}
