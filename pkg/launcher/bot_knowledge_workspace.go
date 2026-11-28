package launcher

import (
	"context"
	"reflect"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
)

// BotKnowledgeHomePublisher is an optional, trusted retained-Home capability.
// Implementations atomically CAS every expected digest, confine regular Markdown
// files, and run the supplied final authority check while holding their physical
// commit barrier. No model tool or generic child receives this capability.
type BotKnowledgeHomePublisher interface {
	SnapshotBotKnowledge(context.Context, *sandbox.BotHomeBinding) ([]store.BotKnowledgeFileSummary, error)
	PublishBotKnowledge(context.Context, *sandbox.BotHomeBinding, store.BotKnowledgeExtractionLease, store.BotKnowledgeExtraction, func(context.Context) error) ([]store.BotKnowledgeFileReadback, error)
	VerifyBotKnowledge(context.Context, *sandbox.BotHomeBinding, store.BotKnowledgeExtractionLease, store.BotKnowledgeExtraction, []store.BotKnowledgeFileReadback) error
}

type BotKnowledgeWorkspace struct {
	Backend BotKnowledgeHomePublisher
	Bots    store.BotStore
}

func (w *BotKnowledgeWorkspace) binding(c context.Context, b store.BotID) (*sandbox.BotHomeBinding, error) {
	if w == nil || w.Backend == nil || w.Bots == nil {
		return nil, store.ErrBotUnsupported
	}
	p, ok := execution.PrincipalFromContext(c)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser {
		return nil, store.ErrBotForbidden
	}
	owned, e := w.Bots.Get(c, b)
	if e != nil {
		return nil, e
	}
	if owned == nil || owned.OwnerID.String() != p.Subject {
		return nil, store.ErrBotForbidden
	}
	h := owned.Home
	if h.ID == uuid.Nil || h.BotID != b || h.Scope != owned.Scope || h.Revision < 1 {
		return nil, store.ErrBotUnsupported
	}
	binding := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: p.OrgSlug, TeamID: p.TeamSlug, UserID: p.Subject, BotID: b.String()}, Backend: sandbox.BackendKind(h.Backend), Ref: h.StorageKey}
	if binding.Validate() != nil {
		return nil, store.ErrBotConflict
	}
	return binding, nil
}

// PublishCommitted resolves the immutable scoped Home before taking the database
// lock; callbacks use that binding without outer-store reads or nested transactions.
func (w *BotKnowledgeWorkspace) PublishCommitted(c context.Context, l store.BotKnowledgeExtractionLease, r store.BotKnowledgeExtraction) error {
	h, err := w.binding(c, l.BotID)
	if err != nil {
		return err
	}
	return entstore.PublishBotKnowledgeExtraction(c, w.Bots, l, r.Revision,
		func(ctx context.Context, current store.BotKnowledgeExtraction, check func(context.Context) error) ([]store.BotKnowledgeFileReadback, error) {
			// The repository owns the root transaction for this entire callback.
			// Approving the helper must not open a second transaction or release
			// that lock before its physical rename/readback has finished.
			ctx = sandbox.WithBotKnowledgeCommitBarrier(ctx, func(commit func() error) error {
				if err := check(ctx); err != nil {
					return err
				}
				return commit()
			})
			return w.Backend.PublishBotKnowledge(ctx, h, l, current, check)
		}, func(ctx context.Context, lease store.BotKnowledgeExtractionLease, current store.BotKnowledgeExtraction, readback []store.BotKnowledgeFileReadback) error {
			return w.Backend.VerifyBotKnowledge(ctx, h, lease, current, readback)
		})
}

func (w *BotKnowledgeWorkspace) Snapshot(c context.Context, b store.BotID) ([]store.BotKnowledgeFileSummary, error) {
	h, e := w.binding(c, b)
	if e != nil {
		return nil, e
	}
	return w.Backend.SnapshotBotKnowledge(c, h)
}
func (w *BotKnowledgeWorkspace) Publish(c context.Context, l store.BotKnowledgeExtractionLease, r store.BotKnowledgeExtraction, check func(context.Context) error) ([]store.BotKnowledgeFileReadback, error) {
	h, e := w.binding(c, l.BotID)
	if e != nil {
		return nil, e
	}
	if r.Decision == nil || r.Decision.Validate() != nil || r.Input.Source.BotID != l.BotID || r.ID != l.ExtractionID || check == nil {
		return nil, store.ErrBotInvalid
	}
	if e = check(c); e != nil {
		return nil, e
	}
	return w.Backend.PublishBotKnowledge(c, h, l, r.Clone(), check)
}
func (w *BotKnowledgeWorkspace) Verify(c context.Context, l store.BotKnowledgeExtractionLease, r store.BotKnowledgeExtraction, readback []store.BotKnowledgeFileReadback) error {
	h, e := w.binding(c, l.BotID)
	if e != nil {
		return e
	}
	if r.Decision == nil {
		return store.ErrBotInvalid
	}
	expected := make([]store.BotKnowledgeFileReadback, 0, len(r.Decision.Proposals))
	for _, p := range r.Decision.Proposals {
		expected = append(expected, store.BotKnowledgeFileReadback{Path: p.Path, Digest: (sandbox.BotHomeWorkspaceFile{Data: []byte(p.Content)}).Digest()})
	}
	if !reflect.DeepEqual(expected, readback) {
		return store.ErrBotConflict
	}
	return w.Backend.VerifyBotKnowledge(c, h, l, r.Clone(), readback)
}
