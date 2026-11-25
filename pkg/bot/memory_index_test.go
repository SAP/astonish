package bot

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type documentFiles struct {
	store.BotFiles
	mu       sync.Mutex
	id       store.BotID
	data     map[string]string
	conflict bool
	writes   int
}

func (f *documentFiles) Open(_ context.Context, id store.BotID, name string, r store.BotFileRange) (*store.BotFileStream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id != f.id {
		return nil, store.ErrBotForbidden
	}
	text, ok := f.data[name]
	if !ok {
		return nil, store.ErrBotNotFound
	}
	entry := store.BotFileEntry{Path: name, Kind: store.BotFileRegular, SizeBytes: int64(len(text)), SHA256: memoryIndexDigest([]byte(text))}
	if r.Length > 0 && int64(len(text)) > r.Length {
		text = text[:r.Length]
	}
	return &store.BotFileStream{ReadCloser: io.NopCloser(strings.NewReader(text)), Entry: entry}, nil
}
func (f *documentFiles) Write(ctx context.Context, id store.BotID, w store.BotFileWrite) (*store.BotFileEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id != f.id {
		return nil, store.ErrBotForbidden
	}
	old, exists := f.data[w.Path]
	if f.conflict || (w.CreateOnly && exists) || (!w.CreateOnly && (!exists || memoryIndexDigest([]byte(old)) != w.ExpectedSHA256)) {
		return nil, store.ErrBotConflict
	}
	data, err := io.ReadAll(w.Body)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != w.SizeBytes {
		return nil, store.ErrBotInvalid
	}
	f.data[w.Path] = string(data)
	f.writes++
	return &store.BotFileEntry{Path: w.Path, Kind: store.BotFileRegular, SizeBytes: int64(len(data)), SHA256: memoryIndexDigest(data)}, nil
}

type indexReceiptResolver struct {
	entry MemoryIndexEntry
	err   error
}

func (r *indexReceiptResolver) ResolveMemoryIndexReceipt(_ context.Context, _ store.BotID, key MemoryIndexReceipt) (*MemoryIndexEntry, error) {
	if r.err != nil {
		return nil, r.err
	}
	copy := r.entry
	copy.RunID = key.RunID
	copy.Publication = key.Publication
	return &copy, nil
}
func memoryIndexFixture() (*MemoryIndex, *documentFiles, *indexReceiptResolver) {
	files := &documentFiles{id: uuid.New(), data: map[string]string{"MEMORY.md": "# Notes\n\nOwner prefers plain language.\n"}}
	resolver := &indexReceiptResolver{entry: MemoryIndexEntry{Date: time.Now().UTC(), Goal: "Prepare the requested report", Outcome: "Report file published", Paths: []string{"reports/result.md"}, Status: MemoryIndexCompleted}}
	return &MemoryIndex{Files: files, Receipts: resolver}, files, resolver
}
func TestBotMemoryIndexReceiptsCASAndDedup(t *testing.T) {
	index, files, resolver := memoryIndexFixture()
	key := MemoryIndexReceipt{RunID: uuid.New(), Publication: 1}
	digest, err := index.Update(context.Background(), files.id, key)
	if err != nil {
		t.Fatal(err)
	}
	if digest != memoryIndexDigest([]byte(files.data["MEMORY.md"])) || !strings.HasPrefix(files.data["MEMORY.md"], "# Notes\n\nOwner prefers plain language.\n") {
		t.Fatal("lost notes/digest")
	}
	before := files.writes
	if _, err = index.Update(context.Background(), files.id, key); err != nil || files.writes != before {
		t.Fatal("duplicate wrote", err)
	}
	resolver.err = store.ErrBotForbidden
	if _, err = index.Update(context.Background(), files.id, MemoryIndexReceipt{RunID: uuid.New(), Publication: 1}); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal(err)
	}
	resolver.err = nil
	files.conflict = true
	if _, err = index.Update(context.Background(), files.id, MemoryIndexReceipt{RunID: uuid.New(), Publication: 1}); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal(err)
	}
	if files.writes != before {
		t.Fatal("failed receipt mutated index")
	}
}
func TestBotMemoryIndexConsolidation(t *testing.T) {
	index, files, resolver := memoryIndexFixture()
	resolver.entry.Outcome = strings.Repeat("observed ", 100)
	first := MemoryIndexReceipt{RunID: uuid.New(), Publication: 1}
	if _, err := index.Update(context.Background(), files.id, first); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		if _, err := index.Update(context.Background(), files.id, MemoryIndexReceipt{RunID: uuid.New(), Publication: 1}); err != nil {
			t.Fatal(err)
		}
		if len(files.data["MEMORY.md"]) > BotMemoryBootstrapBytes {
			t.Fatal("unbounded index")
		}
	}
	if len(files.data) < 2 || !strings.Contains(files.data["MEMORY.md"], "memory/index-history/") {
		t.Fatal("missing linked details")
	}
	before := files.writes
	if _, err := index.Update(context.Background(), files.id, first); err != nil || before != files.writes {
		t.Fatal("archived receipt duplicated", err)
	}
}
func TestBotMemoryIndexRejectsUnverifiedAndPreservesOversize(t *testing.T) {
	for _, mutate := range []func(*indexReceiptResolver){
		func(r *indexReceiptResolver) { r.entry.Status = "model-says-done" },
		func(r *indexReceiptResolver) { r.entry.Paths = []string{"../outside"} },
		func(r *indexReceiptResolver) { r.entry.Paths = nil },
	} {
		index, files, resolver := memoryIndexFixture()
		mutate(resolver)
		if _, err := index.Update(context.Background(), files.id, MemoryIndexReceipt{RunID: uuid.New(), Publication: 1}); !errors.Is(err, store.ErrBotInvalid) || files.writes != 0 {
			t.Fatal(err)
		}
	}
	index, files, _ := memoryIndexFixture()
	original := strings.Repeat("authored notes\n", 2000)
	files.data["MEMORY.md"] = original
	if _, err := index.Update(context.Background(), files.id, MemoryIndexReceipt{RunID: uuid.New(), Publication: 1}); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatal(err)
	}
	if files.data["MEMORY.md"] != original || files.writes != 0 {
		t.Fatal("deleted external notes")
	}
	index, files, _ = memoryIndexFixture()
	delete(files.data, "MEMORY.md")
	if _, err := index.Update(context.Background(), files.id, MemoryIndexReceipt{RunID: uuid.New(), Publication: 1}); !errors.Is(err, store.ErrBotNotFound) || files.writes != 0 {
		t.Fatal("initialized absent home", err)
	}
}

type brokenDocumentFiles struct {
	store.BotFiles
	stream *store.BotFileStream
}

func (f brokenDocumentFiles) Open(context.Context, store.BotID, string, store.BotFileRange) (*store.BotFileStream, error) {
	return f.stream, nil
}

type terminalDocumentReader struct {
	io.Reader
	closed bool
}

func (r *terminalDocumentReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		return n, store.ErrBotConflict
	}
	return n, err
}
func (r *terminalDocumentReader) Close() error { r.closed = true; return nil }

func TestDocumentsRejectsTerminalStreamFailure(t *testing.T) {
	// Reading one byte beyond the requested range forces transport EOF and checks
	// its final readback status instead of cancelling immediately at the bound.
	reader := &terminalDocumentReader{Reader: strings.NewReader(strings.Repeat("a", BotMemoryBootstrapBytes+1))}
	files := brokenDocumentFiles{stream: &store.BotFileStream{ReadCloser: reader, Entry: store.BotFileEntry{Path: "MEMORY.md", Kind: store.BotFileRegular, SizeBytes: BotMemoryBootstrapBytes + 10, SHA256: strings.Repeat("a", 64)}}}
	if _, err := readBotDocument(context.Background(), files, uuid.New(), DocumentMemory, BotMemoryBootstrapBytes); !errors.Is(err, store.ErrBotConflict) || !reader.closed {
		t.Fatal("ignored terminal failure", err)
	}
}

func TestBotMemoryIndexInterrupted(t *testing.T) {
	index, files, resolver := memoryIndexFixture()
	resolver.entry.Status = MemoryIndexInterrupted
	resolver.entry.Paths = nil
	resolver.entry.Outcome = "Run stopped before publication"
	if _, err := index.Update(context.Background(), files.id, MemoryIndexReceipt{RunID: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(files.data["MEMORY.md"], `"status": "interrupted"`) {
		t.Fatal("lost interruption")
	}
}
