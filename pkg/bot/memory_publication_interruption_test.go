package bot

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotMemoryIndexSettledPublicationInterruptions(t *testing.T) {
	for _, status := range []store.RunStatus{store.RunCancelled, store.RunInterrupted} {
		for _, partial := range []bool{false, true} {
			name := string(status)
			if partial {
				name += "-partial"
			}
			t.Run(name, func(t *testing.T) {
				_, docs, _ := memoryIndexFixture()
				run := uuid.New()
				ref := store.BotContentRef{Format: 1, Kind: store.BotContentArtifact, Scope: store.BotContentScope{BotID: docs.id, OwnerID: uuid.NewString()}, Key: "snapshots/partial", Revision: 1, SHA256: strings.Repeat("a", 64), SizeBytes: 100}
				artifact := store.BotArtifact{ID: uuid.New(), RelativePath: "outputs/partial.md", ContentRef: &ref, SHA256: ref.SHA256, SizeBytes: ref.SizeBytes}
				missing := ref
				missing.Key = "snapshots/missing"
				absent := store.BotArtifact{ID: uuid.New(), RelativePath: "outputs/missing.md", ContentRef: &missing, SHA256: missing.SHA256, SizeBytes: missing.SizeBytes}
				saved := &store.BotMemoryPublication{Goal: "Finish the design", Status: status, Publication: store.BotPublication{BotID: docs.id, RunID: run, ReportSequence: 7, PublishedAt: time.Now()}}
				if partial {
					saved.Publication.Artifacts = []store.BotArtifact{artifact, absent, artifact, {ID: uuid.New(), RelativePath: "outputs/unverified.md"}}
				}
				files := memoryPublicationFiles{BotFiles: docs, ref: ref, missingKey: missing.Key}
				resolver := PublicationMemoryResolver{Store: memoryPublicationStore{saved: saved}, Files: files}
				index := MemoryIndex{Files: files, Receipts: resolver}
				key := MemoryIndexReceipt{RunID: run, Publication: 7}
				// Cancellation before the index commit never writes a success or delivery.
				cancelled, cancel := context.WithCancel(t.Context())
				cancel()
				if _, err := index.Update(cancelled, docs.id, key); !errors.Is(err, context.Canceled) || docs.writes != 0 {
					t.Fatal("cancelled update wrote memory", err)
				}
				// A CAS failure leaves the same receipt for metadata-only retry.
				docs.conflict = true
				if _, err := index.Update(t.Context(), docs.id, key); !errors.Is(err, store.ErrBotConflict) || docs.writes != 0 {
					t.Fatal("CAS failure wrote memory", err)
				}
				docs.conflict = false
				digest, err := index.Update(t.Context(), docs.id, key)
				if err != nil {
					t.Fatal(err)
				}
				entry, err := resolver.ResolveMemoryIndexReceipt(t.Context(), docs.id, key)
				if err != nil || entry.Status != MemoryIndexInterrupted || strings.Contains(entry.Outcome, "delivered") || !strings.Contains(entry.Outcome, "unfinished") {
					t.Fatalf("overstated outcome: %+v %v", entry, err)
				}
				want := 0
				if partial {
					want = 1
				}
				if len(entry.Paths) != want || len(entry.PublishedRefs) != want {
					t.Fatalf("unverified refs: %+v", entry)
				}
				if partial && (entry.Paths[0] != artifact.RelativePath || entry.PublishedRefs[0] != artifact.ID.String()) {
					t.Fatalf("wrong partial: %+v", entry)
				}
				text := docs.data["MEMORY.md"]
				if strings.Contains(text, "missing.md") || strings.Contains(text, "unverified.md") {
					t.Fatal("nonexistent artifact indexed", text)
				}
				if got, err := index.Update(t.Context(), docs.id, key); err != nil || got != digest || docs.writes != 1 {
					t.Fatal("receipt replay duplicated outcome", err)
				}
			})
		}
	}
}

func TestBotMemoryPublicationRejectsTerminalReadFailure(t *testing.T) {
	id, run := uuid.New(), uuid.New()
	ref := store.BotContentRef{Format: 1, Kind: store.BotContentArtifact, Scope: store.BotContentScope{BotID: id, OwnerID: uuid.NewString()}, Key: "snapshots/partial", Revision: 1, SHA256: strings.Repeat("a", 64), SizeBytes: 100}
	saved := &store.BotMemoryPublication{Status: store.RunInterrupted, Publication: store.BotPublication{BotID: id, RunID: run, ReportSequence: 1, PublishedAt: time.Now(), Artifacts: []store.BotArtifact{{ID: uuid.New(), RelativePath: "partial.md", ContentRef: &ref, SHA256: ref.SHA256, SizeBytes: ref.SizeBytes}}}}
	reader := &terminalDocumentReader{Reader: strings.NewReader("x")}
	resolver := PublicationMemoryResolver{Store: memoryPublicationStore{saved: saved}, Files: memoryPublicationFiles{ref: ref, reader: reader}}
	if _, err := resolver.ResolveMemoryIndexReceipt(t.Context(), id, MemoryIndexReceipt{RunID: run, Publication: 1}); !errors.Is(err, store.ErrBotConflict) || !reader.closed {
		t.Fatal("terminal read error ignored", err)
	}
	// An empty immutable file is still verified by EOF, not a fabricated byte.
	ref.SizeBytes = 0
	saved.Publication.Artifacts[0].SizeBytes = 0
	resolver.Files = memoryPublicationFiles{ref: ref, reader: io.NopCloser(strings.NewReader(""))}
	if _, err := resolver.ResolveMemoryIndexReceipt(t.Context(), id, MemoryIndexReceipt{RunID: run, Publication: 1}); err != nil {
		t.Fatal(err)
	}
}
