package bot

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"unicode/utf8"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// PublicationMemoryResolver accepts only a persisted publication and retained
// immutable evidence. Working files may have changed since delivery.
type PublicationMemoryResolver struct {
	Store store.BotPublicationMemoryStore
	Files store.BotFiles
}

func memoryBrief(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
func (r PublicationMemoryResolver) ResolveMemoryIndexReceipt(ctx context.Context, id store.BotID, key MemoryIndexReceipt) (*MemoryIndexEntry, error) {
	if r.Store == nil || r.Files == nil {
		return nil, store.ErrBotUnsupported
	}
	if id == uuid.Nil || key.RunID == uuid.Nil || key.Publication < 1 {
		return nil, store.ErrBotInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	saved, err := r.Store.ReadMemoryPublication(ctx, id, key.RunID, key.Publication)
	if err != nil {
		return nil, err
	}
	if saved == nil {
		return nil, store.ErrBotNotFound
	}
	p := saved.Publication
	if p.BotID != id || p.RunID != key.RunID || p.ReportSequence != key.Publication || p.Suppressed || p.PublishedAt.IsZero() {
		return nil, store.ErrBotConflict
	}
	entry := &MemoryIndexEntry{RunID: key.RunID, Publication: key.Publication, Date: p.PublishedAt, Goal: memoryBrief(saved.Goal, 512), Outcome: "Committed report published to Bot Home.", Status: MemoryIndexUnfinished}
	if entry.Goal == "" {
		entry.Goal = "Bot assignment"
	}
	switch saved.Status {
	case store.RunSucceeded:
		entry.Status = MemoryIndexCompleted
	case store.RunInterrupted, store.RunCancelled:
		entry.Status = MemoryIndexInterrupted
	case store.RunPartial, store.RunFailed, store.RunWaitingApproval:
	default:
		return nil, store.ErrBotConflict
	}
	seen := map[string]bool{}
	for _, a := range p.Artifacts {
		if a.ContentRef == nil {
			continue
		}
		ref := *a.ContentRef
		if a.ID == uuid.Nil || ref.Validate() != nil || ref.Kind != store.BotContentArtifact || ref.Scope.BotID != id || ref.SHA256 != a.SHA256 || ref.SizeBytes != a.SizeBytes {
			return nil, store.ErrBotConflict
		}
		// A bounded read asks the trusted content service to verify the snapshot;
		// binary bodies never enter the model or the compact memory index.
		stream, e := r.Files.OpenContent(ctx, ref, store.BotFileRange{Length: 1})
		if e != nil {
			// Settled interruptions are still worth remembering when a partial
			// snapshot is absent. Never list the absent file or downgrade scope,
			// integrity, transport or cancellation errors into missing evidence.
			if entry.Status == MemoryIndexInterrupted && (errors.Is(e, store.ErrBotNotFound) || errors.Is(e, fs.ErrNotExist)) {
				continue
			}
			return nil, e
		}
		if stream == nil || stream.ReadCloser == nil {
			return nil, store.ErrBotInvalid
		}
		// Consume through the bounded range's EOF, so terminal verification
		// errors cannot be hidden by immediately cancelling the transport.
		n, readErr := io.Copy(io.Discard, io.LimitReader(stream, 2))
		e = errors.Join(readErr, stream.Close())
		if e != nil {
			return nil, e
		}
		if n != min(ref.SizeBytes, 1) || stream.Entry.Kind != store.BotFileRegular || stream.Entry.SizeBytes != ref.SizeBytes || stream.Entry.SHA256 != ref.SHA256 {
			return nil, store.ErrBotConflict
		}
		artifactID := a.ID.String()
		if !seen[artifactID] {
			entry.PublishedRefs = append(entry.PublishedRefs, artifactID)
			seen[artifactID] = true
		}
		if !seen[a.RelativePath] {
			entry.Paths = append(entry.Paths, a.RelativePath)
			seen[a.RelativePath] = true
		}
	}
	if len(entry.Paths) == 0 && entry.Status == MemoryIndexCompleted {
		entry.Status = MemoryIndexUnfinished
		entry.Outcome = "Committed response recorded; no published file deliverable."
	}
	if entry.Status == MemoryIndexInterrupted {
		entry.Outcome = "Run interrupted; unfinished work."
		if saved.Status == store.RunCancelled {
			entry.Outcome = "Run cancelled; unfinished work."
		}
		if len(entry.Paths) == 0 {
			entry.Outcome += " No verified published file deliverable."
		} else {
			entry.Outcome += " Only the listed verified partial files are retained."
		}
	}
	if !validMemoryIndexEntry(entry, key) {
		return nil, store.ErrBotInvalid
	}
	return entry, nil
}
