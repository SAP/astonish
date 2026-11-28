package bot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// MemoryIndexStatus is a settled recorded outcome, never a model assessment.
type MemoryIndexStatus string

const (
	MemoryIndexCompleted   MemoryIndexStatus = "completed"
	MemoryIndexUnfinished  MemoryIndexStatus = "unfinished"
	MemoryIndexInterrupted MemoryIndexStatus = "interrupted"
)

// MemoryIndexReceipt identifies committed evidence. It carries no model prose.
// Publication zero is reserved for a settled interruption without publication.
type MemoryIndexReceipt struct {
	RunID       store.RunID
	Publication int64
}

// MemoryIndexEntry is supplied exclusively by trusted receipt resolution. Goal
// and outcome are bounded summaries of committed facts, not execution authority.
type MemoryIndexEntry struct {
	RunID         store.RunID       `json:"run_id"`
	Publication   int64             `json:"publication"`
	Date          time.Time         `json:"date"`
	Goal          string            `json:"goal"`
	Outcome       string            `json:"outcome"`
	Paths         []string          `json:"paths"`
	PublishedRefs []string          `json:"published_refs,omitempty"`
	Status        MemoryIndexStatus `json:"status"`
}

// MemoryIndexReceiptResolver must recheck scope and resolve committed publication
// receipts or settled interruption facts, checking every path against retained
// file evidence. Never implement this by decoding model-provided entry JSON.
type MemoryIndexReceiptResolver interface {
	ResolveMemoryIndexReceipt(context.Context, store.BotID, MemoryIndexReceipt) (*MemoryIndexEntry, error)
}

// MemoryIndex writes only an existing MEMORY.md under the BotFiles commit fence.
// It cannot provision Home or grant authority. Retrying the same receipt is safe.
type MemoryIndex struct {
	Files    store.BotFiles
	Receipts MemoryIndexReceiptResolver
}

const memoryIndexStart = "<!-- astonish:memory-index:v1 -->\n"
const memoryIndexEnd = "<!-- /astonish:memory-index -->"
const memoryIndexReadMax = 256 << 10

type memoryIndexSection struct {
	Archive string             `json:"earlier_index,omitempty"`
	Entries []MemoryIndexEntry `json:"entries"`
}

func memoryIndexDigest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func readMemoryIndexFile(ctx context.Context, files store.BotFiles, id store.BotID, name string) ([]byte, error) {
	stream, err := files.Open(ctx, id, name, store.BotFileRange{Length: memoryIndexReadMax + 1})
	if err != nil {
		return nil, err
	}
	if stream == nil || stream.ReadCloser == nil {
		return nil, store.ErrBotInvalid
	}
	data, readErr := io.ReadAll(io.LimitReader(stream, memoryIndexReadMax+2))
	closeErr := stream.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(data) > memoryIndexReadMax || stream.Entry.Kind != store.BotFileRegular || stream.Entry.Path != name || stream.Entry.SizeBytes != int64(len(data)) {
		return nil, store.ErrBotInvalid
	}
	if stream.Entry.SHA256 != "" && stream.Entry.SHA256 != memoryIndexDigest(data) {
		return nil, store.ErrBotConflict
	}
	return data, nil
}

func splitMemoryIndex(text string) (string, string, memoryIndexSection, error) {
	var section memoryIndexSection
	start := strings.Index(text, memoryIndexStart)
	if start < 0 {
		if strings.Contains(text, memoryIndexEnd) {
			return "", "", section, store.ErrBotInvalid
		}
		return text, "", section, nil
	}
	end := strings.Index(text[start+len(memoryIndexStart):], memoryIndexEnd)
	if end < 0 || strings.Count(text, memoryIndexStart) != 1 || strings.Count(text, memoryIndexEnd) != 1 {
		return "", "", section, store.ErrBotInvalid
	}
	end += start + len(memoryIndexStart)
	raw := strings.TrimSpace(text[start+len(memoryIndexStart) : end])
	raw = strings.TrimSuffix(strings.TrimPrefix(raw, "```json\n"), "\n```")
	if err := json.Unmarshal([]byte(raw), &section); err != nil {
		return "", "", section, store.ErrBotInvalid
	}
	return text[:start], text[end+len(memoryIndexEnd):], section, nil
}

func renderMemoryIndex(prefix, suffix string, section memoryIndexSection) string {
	raw, _ := json.MarshalIndent(section, "", "  ")
	return prefix + memoryIndexStart + "```json\n" + string(raw) + "\n```\n" + memoryIndexEnd + suffix
}

func validMemoryIndexEntry(entry *MemoryIndexEntry, key MemoryIndexReceipt) bool {
	if entry == nil || entry.RunID != key.RunID || entry.Publication != key.Publication || key.RunID == uuid.Nil || key.Publication < 0 || entry.Date.IsZero() || len(entry.Goal) > 512 || len(entry.Outcome) > 1024 || strings.TrimSpace(entry.Goal) == "" || strings.TrimSpace(entry.Outcome) == "" || len(entry.Paths) > 16 || len(entry.PublishedRefs) > 16 {
		return false
	}
	switch entry.Status {
	case MemoryIndexCompleted, MemoryIndexUnfinished:
		if key.Publication == 0 {
			return false
		}
	case MemoryIndexInterrupted:
	default:
		return false
	}
	if entry.Status == MemoryIndexCompleted && len(entry.Paths) == 0 {
		return false
	}
	for _, p := range entry.Paths {
		if p == "" || p == "." || path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") || strings.ContainsAny(p, "\\\x00\r\n") || len(p) > 512 {
			return false
		}
	}
	for _, ref := range entry.PublishedRefs {
		if ref == "" || len(ref) > 512 || strings.ContainsAny(ref, "\x00\r\n") {
			return false
		}
	}
	return true
}

func memoryArchivePath(data []byte) string {
	return "memory/index-history/" + memoryIndexDigest(data) + ".md"
}

// Update returns the current/new MEMORY.md digest. On conflict the caller retries
// this receipt only, never the underlying work. Consolidation keeps older entries
// in immutable linked detail files before the bootstrap index reaches 12 KiB.
// Authored text outside the managed block is byte-preserved. Externally oversized
// notes are not deleted or silently rewritten; the update fails closed instead.
func (m *MemoryIndex) Update(ctx context.Context, id store.BotID, key MemoryIndexReceipt) (string, error) {
	if m == nil || m.Files == nil || m.Receipts == nil {
		return "", store.ErrBotUnsupported
	}
	entry, err := m.Receipts.ResolveMemoryIndexReceipt(ctx, id, key)
	if err != nil {
		return "", err
	}
	if !validMemoryIndexEntry(entry, key) {
		return "", store.ErrBotInvalid
	}
	original, err := readMemoryIndexFile(ctx, m.Files, id, string(DocumentMemory))
	if err != nil {
		return "", err
	}
	prefix, suffix, current, err := splitMemoryIndex(string(original))
	if err != nil {
		return "", err
	}
	// Follow only content-addressed archives produced by this writer. A malformed
	// or excessively deep chain must not be treated as absence of an old receipt.
	section := current
	visited := map[string]bool{}
	for depth := 0; ; depth++ {
		for _, old := range section.Entries {
			if old.RunID == key.RunID && old.Publication == key.Publication {
				return memoryIndexDigest(original), nil
			}
		}
		if section.Archive == "" {
			break
		}
		if depth >= 255 || visited[section.Archive] {
			return "", store.ErrBotInvalid
		}
		visited[section.Archive] = true
		name := section.Archive
		if !strings.HasPrefix(name, "memory/index-history/") || path.Clean(name) != name || len(strings.TrimSuffix(strings.TrimPrefix(name, "memory/index-history/"), ".md")) != 64 {
			return "", store.ErrBotInvalid
		}
		data, err := readMemoryIndexFile(ctx, m.Files, id, name)
		if err != nil {
			return "", err
		}
		if memoryArchivePath(data) != name {
			return "", store.ErrBotConflict
		}
		_, _, section, err = splitMemoryIndex(string(data))
		if err != nil {
			return "", err
		}
	}
	next := memoryIndexSection{Archive: current.Archive, Entries: append([]MemoryIndexEntry{*entry}, current.Entries...)}
	text := renderMemoryIndex(prefix, suffix, next)
	if len(text) > BotMemoryBootstrapBytes {
		archive := []byte(renderMemoryIndex("", "", current))
		name := memoryArchivePath(archive)
		next = memoryIndexSection{Archive: name, Entries: []MemoryIndexEntry{*entry}}
		text = renderMemoryIndex(prefix, suffix, next)
		if len(text) > BotMemoryBootstrapBytes {
			return "", fmt.Errorf("%w: authored MEMORY.md notes require explicit consolidation", store.ErrBotInvalid)
		}
		_, err = m.Files.Write(ctx, id, store.BotFileWrite{Path: name, SizeBytes: int64(len(archive)), Body: strings.NewReader(string(archive)), CreateOnly: true})
		if errors.Is(err, store.ErrBotConflict) {
			retained, readErr := readMemoryIndexFile(ctx, m.Files, id, name)
			if readErr != nil {
				return "", readErr
			}
			if string(retained) != string(archive) {
				return "", store.ErrBotConflict
			}
		} else if err != nil {
			return "", err
		}
	}
	_, err = m.Files.Write(ctx, id, store.BotFileWrite{Path: string(DocumentMemory), SizeBytes: int64(len(text)), Body: strings.NewReader(text), ExpectedSHA256: memoryIndexDigest(original)})
	if err != nil {
		return "", err
	}
	return memoryIndexDigest([]byte(text)), nil
}
