package entstore

import (
	"context"
	"io"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

type repositoryHelperResource struct{ backend *repositoryHelperBackend }

func (r *repositoryHelperResource) Close() error { r.backend.closes++; return nil }

type repositoryHelperBackend struct {
	sandbox.BotHomeFilesBackend
	creates, closes, reads, writes int
	operations                     map[*sandbox.BotHomeFileOperation]bool
}

func (b *repositoryHelperBackend) borrow(ctx context.Context, write bool) (func(bool) error, error) {
	op := sandbox.BotHomeFileOperationFromContext(ctx)
	if op == nil {
		return nil, store.ErrBotUnsupported
	}
	b.operations[op] = true
	_, _, release, err := op.Borrow(ctx, write, func(context.Context) (sandbox.BotHomeFileResource, error) {
		b.creates++
		return &repositoryHelperResource{backend: b}, nil
	})
	return release, err
}
func (b *repositoryHelperBackend) OpenBotContent(ctx context.Context, home *sandbox.BotHomeBinding, ref store.BotContentRef, span store.BotFileRange) (*store.BotFileStream, error) {
	b.reads++
	release, err := b.borrow(ctx, false)
	if err != nil {
		return nil, err
	}
	stream, err := b.BotHomeFilesBackend.OpenBotContent(ctx, home, ref, span)
	// This in-memory backend completes transport before returning its byte reader.
	if closeErr := release(err == nil); err == nil {
		err = closeErr
	}
	return stream, err
}
func (b *repositoryHelperBackend) PutBotContent(ctx context.Context, home *sandbox.BotHomeBinding, kind store.BotContentKind, size int64, body io.Reader, revision int64, guard func(func() error) error) (*store.BotContentRef, error) {
	b.writes++
	release, err := b.borrow(ctx, true)
	if err != nil {
		return nil, err
	}
	ref, err := b.BotHomeFilesBackend.PutBotContent(ctx, home, kind, size, body, revision, guard)
	if closeErr := release(err == nil); err == nil {
		err = closeErr
	}
	return ref, err
}

// No manually derived operation context is passed here. SaveGoal's production
// closure captures the original request ctx, just like LoadWork and SaveWork.
func TestBotRepositoryClosureHelperReuse(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, bot, exact, backend := selectionFixture(t, scope)
			if err := repo.selectHomeConfiguration(ctx, exact, backend); err != nil {
				t.Fatal(err)
			}
			counted := &repositoryHelperBackend{BotHomeFilesBackend: backend, operations: map[*sandbox.BotHomeFileOperation]bool{}}
			wrap := func(c *botContentCodec) *botContentCodec {
				return &botContentCodec{OpenIntake: c.OpenIntake, Open: func(ctx context.Context, id store.BotID) (store.BotContentStaging, error) {
					disk, err := c.Open(ctx, id)
					if h, ok := disk.(*botHomeContent); ok && err == nil {
						h.backend = counted
					}
					return disk, err
				}}
			}
			switch db := repo.db.(type) {
			case personalBotDB:
				db.content = wrap(db.content)
				repo.db = db
			case teamBotDB:
				db.content = wrap(db.content)
				repo.db = db
			}
			goal := store.BotGoal{BotID: bot.ID, Title: "Helper routing", CompletionCriteria: "Stored and reloaded", Description: "first bytes"}
			var revision int64
			for i := 1; i <= 2; i++ {
				saved, err := repo.SaveGoal(ctx, store.BotGoalUpdate{BotID: bot.ID, Goal: goal, ExpectedRevision: revision})
				if err != nil {
					t.Fatalf("production closure lost helper scope: %v", err)
				}
				goal = *saved
				revision = saved.Revision
				goal.Description = "updated bytes"
			}
			// Goals remain file-based: each save opens its own transaction-scoped
			// backend and writes the goal body. Runtime records never touch the backend.
			if counted.writes != 2 || counted.closes != counted.creates {
				t.Fatalf("did not exercise repeated backend IO: %+v", counted)
			}
			if sandbox.BotHomeFileOperationFromContext(ctx) != nil {
				t.Fatal("transaction scope escaped onto caller context")
			}
		})
	}
}
