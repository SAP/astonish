package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	adkmodel "google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

// MaxTranscriptLineBytes bounds a single JSONL record when reading a transcript.
// A line larger than this is skipped rather than aborting the whole file, so one
// pathological tool response cannot make an entire session unreadable.
const MaxTranscriptLineBytes = 10 * 1024 * 1024

// Transcript handles reading and writing JSONL session transcript files.
// Each line in the file is a JSON-serialized TranscriptEntry.
type Transcript struct {
	path string
}

// TranscriptEntry is a single line in the JSONL transcript file.
type TranscriptEntry struct {
	Type  string            `json:"type"`            // "header" or "event"
	Event *adksession.Event `json:"event,omitempty"` // For "event" type
	// Header fields
	SessionID string `json:"sessionId,omitempty"` // For "header" type
	Version   int    `json:"version,omitempty"`   // For "header" type
}

// NewTranscript creates a Transcript for the given file path.
// The path is cleaned and validated to prevent path traversal.
func NewTranscript(path string) *Transcript {
	cleaned := filepath.Clean(path)
	if strings.Contains(cleaned, "..") {
		// Return a transcript with an empty path; operations will fail safely
		return &Transcript{path: ""}
	}
	return &Transcript{path: cleaned}
}

// WriteHeader writes the initial header line to a new transcript file.
func (t *Transcript) WriteHeader(sessionID string) error {
	dir := filepath.Dir(t.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create transcript directory: %w", err)
	}

	entry := TranscriptEntry{
		Type:      "header",
		SessionID: sessionID,
		Version:   1,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to serialize header: %w", err)
	}

	return os.WriteFile(t.path, append(data, '\n'), 0644)
}

// AppendEvent appends a single event to the transcript file.
func (t *Transcript) AppendEvent(event *adksession.Event) error {
	return t.appendEventData(event, nil)
}

// AppendEventRedacted appends a single event to the transcript file,
// applying a redaction function to the serialized JSON before writing.
// This ensures credential values are never written to disk.
func (t *Transcript) AppendEventRedacted(event *adksession.Event, redactFunc func(string) string) error {
	return t.appendEventData(event, redactFunc)
}

func (t *Transcript) appendEventData(event *adksession.Event, redactFunc func(string) string) error {
	entry := TranscriptEntry{
		Type:  "event",
		Event: event,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to serialize event: %w", err)
	}

	// Apply redaction to the serialized JSON if configured
	if redactFunc != nil {
		data = []byte(redactFunc(string(data)))
	}

	data = append(data, '\n')

	f, err := os.OpenFile(t.path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("failed to open transcript for append: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("failed to append event: %w", err)
	}

	return f.Sync()
}

// ReadEvents reads all events from the transcript file (skipping the header).
// Returns events in chronological order.
func (t *Transcript) ReadEvents() ([]*adksession.Event, error) {
	f, err := os.Open(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to open transcript: %w", err)
	}
	defer f.Close()

	var events []*adksession.Event

	skipped, skippedBytes, err := scanTranscriptLines(f, func(line []byte) {
		if len(line) == 0 {
			return
		}
		var entry TranscriptEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			// Skip malformed lines
			return
		}
		if entry.Type == "event" && entry.Event != nil {
			events = append(events, entry.Event)
		}
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("error reading transcript: %w", err)
	}

	if skipped > 0 {
		slog.Warn("transcript: skipped oversized lines",
			"component", "session", "path", t.path,
			"skipped", skipped, "bytes", skippedBytes)
		events = append(events, oversizedWarningEvent(skipped, skippedBytes))
	}

	return events, nil
}

// ScanOversized counts oversized lines in the transcript without decoding events.
func (t *Transcript) ScanOversized() (int, int64, error) {
	f, err := os.Open(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("failed to open transcript: %w", err)
	}
	defer f.Close()

	return scanTranscriptLines(f, func([]byte) {}, nil)
}

// oversizedWarningEvent builds the synthetic system event appended when lines
// had to be skipped, so the gap is visible in the restored transcript.
func oversizedWarningEvent(skipped int, skippedBytes int64) *adksession.Event {
	msg := fmt.Sprintf("⚠ %d transcript line(s) totaling %s exceeded the %s read limit and were skipped. Run 'astonish sessions repair <id>' to rewrite this transcript.",
		skipped, humanBytes(skippedBytes), humanBytes(int64(MaxTranscriptLineBytes)))
	return &adksession.Event{
		ID:        "transcript-skipped-lines",
		Author:    "system",
		Timestamp: time.Now(),
		Actions:   adksession.EventActions{},
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText(msg, genai.RoleModel),
		},
	}
}

func humanBytes(n int64) string {
	const mb = 1024 * 1024
	switch {
	case n >= 1024*mb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(1024*mb))
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= 1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// scanTranscriptLines reads newline-delimited records with bounded memory.
// Lines up to MaxTranscriptLineBytes are passed whole to onLine. Larger lines
// are never retained: they are counted and streamed to onOversizedChunk (which
// may be nil to drop them), so a single huge record cannot abort the read or
// exhaust memory. A trailing record without a final newline is handled normally.
func scanTranscriptLines(rd io.Reader, onLine func(line []byte), onOversizedChunk func(chunk []byte, final bool)) (int, int64, error) {
	r := bufio.NewReaderSize(rd, 64*1024)

	var (
		skipped      int
		skippedBytes int64
		acc          []byte
		accLen       int64
		over         bool
	)

	flushLine := func() {
		if over {
			skipped++
			skippedBytes += accLen
			if onOversizedChunk != nil {
				onOversizedChunk(nil, true)
			}
		} else if onLine != nil {
			onLine(acc)
		}
		acc = acc[:0]
		accLen = 0
		over = false
	}

	for {
		chunk, err := r.ReadSlice('\n')
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) && !errors.Is(err, io.EOF) {
			return skipped, skippedBytes, err
		}

		hasNewline := err == nil
		data := chunk
		if hasNewline {
			data = chunk[:len(chunk)-1]
		}

		accLen += int64(len(data))
		if !over && accLen > MaxTranscriptLineBytes {
			over = true
			// Hand off whatever prefix we already buffered, then stop retaining.
			if onOversizedChunk != nil && len(acc) > 0 {
				onOversizedChunk(acc, false)
			}
			acc = nil
		}
		if over {
			if onOversizedChunk != nil {
				onOversizedChunk(data, false)
			}
		} else {
			acc = append(acc, data...)
		}

		if hasNewline {
			flushLine()
			continue
		}

		if errors.Is(err, io.EOF) {
			if accLen > 0 || over {
				flushLine()
			}
			return skipped, skippedBytes, nil
		}
		// ErrBufferFull: keep accumulating this line.
	}
}

// EventCount returns the number of events in the transcript (excluding header).
func (t *Transcript) EventCount() int {
	events, err := t.ReadEvents()
	if err != nil {
		return 0
	}
	return len(events)
}

// Exists checks if the transcript file exists.
func (t *Transcript) Exists() bool {
	_, err := os.Stat(t.path)
	return err == nil
}

// Rewrite atomically replaces the transcript with a new header and events.
// Used after compaction to persist the compacted conversation on disk.
func (t *Transcript) Rewrite(sessionID string, events []*adksession.Event) error {
	// Build new content in memory
	var lines [][]byte

	// Header
	header := TranscriptEntry{
		Type:      "header",
		SessionID: sessionID,
		Version:   1,
	}
	headerData, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("failed to serialize header: %w", err)
	}
	lines = append(lines, headerData)

	// Events
	for _, event := range events {
		entry := TranscriptEntry{
			Type:  "event",
			Event: event,
		}
		data, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("failed to serialize event: %w", err)
		}
		lines = append(lines, data)
	}

	// Build the final content
	var content []byte
	for _, line := range lines {
		content = append(content, line...)
		content = append(content, '\n')
	}

	// Atomic write to prevent corruption
	return atomicWrite(t.path, content, 0644)
}

// RedactTranscript retroactively applies a redaction function to every line
// of the transcript file and atomically rewrites it. This is used after new
// credential values are registered (e.g. after save_credential) to scrub
// secrets from user messages that were persisted before the secret was known.
func (t *Transcript) RedactTranscript(redactFunc func(string) string) error {
	if redactFunc == nil {
		return nil
	}

	f, err := os.Open(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to open transcript for redaction: %w", err)
	}
	defer f.Close()

	var content []byte
	changed := false

	_, _, err = scanTranscriptLines(f, func(line []byte) {
		s := string(line)
		redacted := redactFunc(s)
		if redacted != s {
			changed = true
		}
		content = append(content, []byte(redacted)...)
		content = append(content, '\n')
	}, func(chunk []byte, final bool) {
		// Oversized lines are passed through unmodified — redaction must never
		// silently delete content; only `sessions repair` removes lines.
		if final {
			content = append(content, '\n')
			return
		}
		content = append(content, chunk...)
	})
	if err != nil {
		return fmt.Errorf("error reading transcript for redaction: %w", err)
	}

	// Only rewrite if something actually changed
	if !changed {
		return nil
	}

	return atomicWrite(t.path, content, 0644)
}

// RepairOversized rewrites the transcript without its oversized lines. Every
// retained line is copied verbatim (including the header), so the JSONL format
// and header contract are preserved byte-for-byte. The current file is copied
// to backupPath before the rewrite. When no line is oversized it makes no
// backup and performs no write.
func (t *Transcript) RepairOversized(backupPath string) (int, int64, error) {
	f, err := os.Open(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("failed to open transcript: %w", err)
	}

	var kept []byte
	skipped, skippedBytes, err := scanTranscriptLines(f, func(line []byte) {
		kept = append(kept, line...)
		kept = append(kept, '\n')
	}, nil)
	f.Close()
	if err != nil {
		return 0, 0, fmt.Errorf("error reading transcript: %w", err)
	}
	if skipped == 0 {
		return 0, 0, nil
	}

	orig, err := os.ReadFile(t.path)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read transcript for backup: %w", err)
	}
	if err := os.WriteFile(backupPath, orig, 0644); err != nil {
		return 0, 0, fmt.Errorf("failed to write backup: %w", err)
	}

	if err := atomicWrite(t.path, kept, 0644); err != nil {
		return 0, 0, fmt.Errorf("failed to rewrite transcript: %w", err)
	}
	return skipped, skippedBytes, nil
}

// HumanBytes formats a byte count for CLI output.
func HumanBytes(n int64) string { return humanBytes(n) }
