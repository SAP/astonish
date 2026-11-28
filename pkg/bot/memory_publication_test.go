package bot

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"io"
	"strings"
	"testing"
	"time"
)

type memoryPublicationStore struct {
	store.BotPublicationMemoryStore
	saved *store.BotMemoryPublication
	err   error
}

func (s memoryPublicationStore) ReadMemoryPublication(context.Context, store.BotID, store.RunID, int64) (*store.BotMemoryPublication, error) {
	return s.saved, s.err
}

type memoryPublicationFiles struct {
	store.BotFiles
	ref        store.BotContentRef
	err        error
	missingKey string
	reader     io.ReadCloser
}

func (f memoryPublicationFiles) OpenContent(_ context.Context, r store.BotContentRef, _ store.BotFileRange) (*store.BotFileStream, error) {
	if f.err != nil {
		return nil, f.err
	}
	if r.Key == f.missingKey {
		return nil, store.ErrBotNotFound
	}
	if r != f.ref {
		return nil, store.ErrBotConflict
	}
	reader := f.reader
	if reader == nil {
		reader = io.NopCloser(strings.NewReader(strings.Repeat("x", int(min(r.SizeBytes, 1)))))
	}
	return &store.BotFileStream{Entry: store.BotFileEntry{Kind: store.BotFileRegular, SizeBytes: r.SizeBytes, SHA256: r.SHA256}, ReadCloser: reader}, nil
}
func TestBotMemoryPublicationReceipt(t *testing.T) {
	id, run := uuid.New(), uuid.New()
	ref := store.BotContentRef{Format: 1, Kind: store.BotContentArtifact, Scope: store.BotContentScope{BotID: id, OwnerID: uuid.NewString()}, Key: "runs/report", Revision: 1, SHA256: strings.Repeat("a", 64), SizeBytes: 100}
	artifact := store.BotArtifact{ID: uuid.New(), RelativePath: "outputs/report.md", ContentRef: &ref, SHA256: ref.SHA256, SizeBytes: 100}
	saved := store.BotMemoryPublication{Goal: "Create report", Status: store.RunSucceeded, Publication: store.BotPublication{BotID: id, RunID: run, ReportSequence: 7, PublishedAt: time.Now(), Artifacts: []store.BotArtifact{artifact, artifact}}}
	key := MemoryIndexReceipt{RunID: run, Publication: 7}
	for _, mode := range []string{"committed", "uncommitted", "suppressed", "stale-sequence", "stale-file", "wrong-bot", "interrupted", "running", "missing-file", "forbidden-file", "cancelled-file"} {
		t.Run(mode, func(t *testing.T) {
			copy := saved
			copy.Publication = saved.Publication
			repo := memoryPublicationStore{saved: &copy}
			files := memoryPublicationFiles{ref: ref}
			switch mode {
			case "uncommitted":
				repo.err = store.ErrBotNotFound
			case "suppressed":
				copy.Publication.Suppressed = true
			case "stale-sequence":
				copy.Publication.ReportSequence++
			case "stale-file":
				files.err = store.ErrBotConflict
			case "wrong-bot":
				copy.Publication.BotID = uuid.New()
			case "running":
				copy.Status = store.RunRunning
			case "missing-file":
				files.err = store.ErrBotNotFound
			case "forbidden-file":
				copy.Status = store.RunInterrupted
				files.err = store.ErrBotForbidden
			case "cancelled-file":
				copy.Status = store.RunInterrupted
				files.err = context.Canceled
			case "interrupted":
				copy.Status = store.RunInterrupted
				copy.Publication.Artifacts = nil
			}
			entry, err := (PublicationMemoryResolver{Store: repo, Files: files}).ResolveMemoryIndexReceipt(t.Context(), id, key)
			if mode != "committed" && mode != "interrupted" {
				if err == nil {
					t.Fatal("unverified receipt indexed")
				}
				return
			}
			if err != nil || !validMemoryIndexEntry(entry, key) {
				t.Fatalf("entry %+v %v", entry, err)
			}
			if mode == "committed" && (len(entry.Paths) != 1 || len(entry.PublishedRefs) != 1 || entry.Status != MemoryIndexCompleted) {
				t.Fatalf("not deduplicated: %+v", entry)
			}
			if mode == "interrupted" && (entry.Status != MemoryIndexInterrupted || strings.Contains(entry.Outcome, "delivered")) {
				t.Fatalf("interruption overstated: %+v", entry)
			}
		})
	}
}
