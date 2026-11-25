package entstore

import (
	"context"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotContentOperationObserverIsPayloadFree(t *testing.T) {
	var events []BotExecutionTiming
	ctx := WithBotExecutionObserver(t.Context(), func(v BotExecutionTiming) { events = append(events, v) })
	bound := CopyImmutableReadWindow(context.Background(), ctx)
	ObserveBotExecutionStage(bound, BotStageBookkeeping)(store.ErrBotInvalid)
	ObserveBotExecutionStage(bound, BotStageProvider)(nil)
	if len(events) != 2 || events[0].Result != BotStageFailed || events[1].Result != BotStageSucceeded || events[0].Duration < 0 {
		t.Fatalf("timings=%+v", events)
	}
}

func TestBotContentOperationObserverIntervalsAndErrors(t *testing.T) {
	var events []BotExecutionTiming
	ctx := WithBotExecutionObserver(t.Context(), func(v BotExecutionTiming) { events = append(events, v) })
	outer := ObserveBotExecutionStage(ctx, BotStageStartup)
	inner := ObserveBotExecutionStage(ctx, BotStagePreflight)
	inner(store.ErrBotInvalid)
	inner(nil)
	outer(nil)
	if len(events) != 2 || events[0].Stage != BotStagePreflight || events[1].Stage != BotStageStartup || events[0].Result != BotStageFailed {
		t.Fatalf("events=%+v", events)
	}
	for _, e := range events {
		if e.Duration < 0 || e.Duration != e.EndedAt.Sub(e.StartedAt) || e.StartedAt == e.StartedAt.Round(0) {
			t.Fatalf("interval=%+v", e)
		}
	}
	if events[0].StartedAt.Before(events[1].StartedAt) || events[0].EndedAt.After(events[1].EndedAt) {
		t.Fatal("nested interval escaped enclosing span")
	}
	ObserveBotExecutionStageSince(ctx, BotStageQueue, time.Now().Add(time.Hour))(nil)
	if events[2].Duration != 0 {
		t.Fatal("future clock produced negative interval")
	}
}

func TestBotContentOperationRepositoryFailureStages(t *testing.T) {
	var events []BotExecutionTiming
	ctx := WithBotExecutionObserver(t.Context(), func(v BotExecutionTiming) { events = append(events, v) })
	r := &botRepository{}
	_, claimErr := r.ClaimRun(ctx, store.BotClaimRequest{})
	_, approvalErr := r.RequestApproval(ctx, store.BotLease{}, store.BotApproval{})
	commitErr := r.CommitRun(ctx, store.BotRunCommit{ModelSelection: &store.SessionPin{}})
	if claimErr == nil || approvalErr == nil || commitErr == nil || len(events) != 3 {
		t.Fatalf("events=%+v", events)
	}
	for i, stage := range []BotExecutionStage{BotStageClaim, BotStageApproval, BotStageCommit} {
		if events[i].Stage != stage || events[i].Result != BotStageFailed {
			t.Fatalf("events=%+v", events)
		}
	}
}

func TestBotContentImmutableReadWindowLifetime(t *testing.T) {
	type identityKey struct{}
	owner := context.WithValue(t.Context(), identityKey{}, "owner")
	agent := WithImmutableReadWindow(context.WithValue(t.Context(), identityKey{}, "agent"))
	bound := CopyImmutableReadWindow(owner, agent)
	if bound.Value(identityKey{}) != "owner" || activeImmutableReadWindow(bound) != nil {
		t.Fatal("empty window copied authority or enabled byte reuse")
	}
	BeginImmutableReadWindow(agent)
	first := activeImmutableReadWindow(bound)
	if first == nil || first != activeImmutableReadWindow(agent) {
		t.Fatal("owner and agent did not share the active window")
	}
	BeginImmutableReadWindow(bound)
	if activeImmutableReadWindow(agent) != first {
		t.Fatal("nested begin replaced active bytes")
	}
	borrowed, closeBorrowed := WithBotContentReadOperationIfAbsent(bound)
	if borrowed.Value(botContentReadOperationKey{}) != first {
		t.Fatal("bookkeeping did not borrow gap bytes")
	}
	closeBorrowed()
	if activeImmutableReadWindow(agent) != first {
		t.Fatal("borrower ended gap")
	}
	EndImmutableReadWindow(bound)
	if WithBotContentReadOperationFromContext(context.Background(), borrowed) != context.Background() {
		t.Fatal("retained borrower escaped closed gap")
	}
	EndImmutableReadWindow(agent)
	if activeImmutableReadWindow(agent) != nil || !first.closed || first.objects != nil || first.bytes != 0 {
		t.Fatal("ended window retained bytes")
	}
	BeginImmutableReadWindow(agent)
	defer EndImmutableReadWindow(agent)
	if next := activeImmutableReadWindow(bound); next == nil || next == first {
		t.Fatal("next provider gap reused the ended window")
	}
}

func TestBotContentOperationPreflightOnlyBorrowsExplicitGap(t *testing.T) {
	old, endOld := WithBotContentReadOperation(t.Context())
	defer endOld()
	fresh, endFresh := WithBotContentReadPreflight(old)
	if fresh.Value(botContentReadOperationKey{}) == old.Value(botContentReadOperationKey{}) {
		t.Fatal("retained operation borrowed by producer")
	}
	endFresh()
	gap := WithImmutableReadWindow(old)
	BeginImmutableReadWindow(gap)
	defer EndImmutableReadWindow(gap)
	prepared, end := WithBotContentReadPreflight(gap)
	defer end()
	if prepared.Value(botContentReadOperationKey{}) != activeImmutableReadWindow(gap) {
		t.Fatal("explicit gap not borrowed")
	}
}

func TestBotContentSelectionReadsEachDestinationOnce(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, b, exact, backend := selectionFixture(t, scope)
			seedHomeSelectionCategories(t, r, ctx, b)
			counted := &transactionCountingBackend{BotHomeFilesBackend: backend}
			wrap := func(c *botContentCodec) *botContentCodec {
				return &botContentCodec{OpenIntake: c.OpenIntake, Open: func(ctx context.Context, id store.BotID) (store.BotContentStaging, error) {
					disk, err := c.Open(ctx, id)
					if h, ok := disk.(*botHomeContent); ok && err == nil {
						h.backend = counted
					}
					return disk, err
				}}
			}
			switch d := r.db.(type) {
			case personalBotDB:
				d.content = wrap(d.content)
				r.db = d
			case teamBotDB:
				d.content = wrap(d.content)
				r.db = d
			}
			if err := r.selectHomeConfiguration(ctx, exact, counted); err != nil {
				t.Fatal(err)
			}
			t.Logf("destination opens=%d unique objects=%d", counted.opens, len(backend.objects))
			if counted.opens != len(backend.objects) {
				t.Fatal("selection reopened already verified destinations")
			}
			backend.corrupt = true
			if _, err := r.db.ReadVersion(ctx, b.ID, 1); err == nil {
				t.Fatal("selection cache escaped its operation")
			}
		})
	}
}

func TestBotContentReadOperationTransferIsolation(t *testing.T) {
	type ownerKey struct{}
	owner := context.WithValue(t.Context(), ownerKey{}, "captured owner")
	source, end := WithBotContentReadOperation(context.WithValue(t.Context(), ownerKey{}, "ambient viewer"))
	defer end()
	bound := WithBotContentReadOperationFromContext(owner, source)
	if bound.Value(ownerKey{}) != "captured owner" {
		t.Fatal("operation transfer copied ambient authority")
	}
	if bound.Value(botContentReadOperationKey{}) != source.Value(botContentReadOperationKey{}) {
		t.Fatal("operation transfer lost byte cache")
	}
	if WithBotContentReadOperationFromContext(owner, nil) != owner {
		t.Fatal("nil source changed owner")
	}
}

func TestBotContentReadOperationNestedLifetime(t *testing.T) {
	outer, end := WithBotContentReadOperation(t.Context())
	inner, closeInner := WithBotContentReadOperationIfAbsent(outer)
	if inner.Value(botContentReadOperationKey{}) != outer.Value(botContentReadOperationKey{}) {
		t.Fatal("nested synchronous operation discarded parent's immutable reads")
	}
	closeInner()
	if WithBotContentReadOperationFromContext(context.Background(), outer) == context.Background() {
		t.Fatal("borrower closed owner's operation")
	}
	end()
	if WithBotContentReadOperationFromContext(context.Background(), inner) != context.Background() {
		t.Fatal("nested operation survived owner")
	}
}

func TestBotContentReadOperationLifetime(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, b, exact, backend := selectionFixture(t, scope)
			if err := r.selectHomeConfiguration(ctx, exact, backend); err != nil {
				t.Fatal(err)
			}
			counted := &transactionCountingBackend{BotHomeFilesBackend: backend}
			lookups := 0
			wrap := func(c *botContentCodec) *botContentCodec {
				return &botContentCodec{OpenIntake: c.OpenIntake, Open: func(ctx context.Context, id store.BotID) (store.BotContentStaging, error) {
					lookups++
					disk, err := c.Open(ctx, id)
					if h, ok := disk.(*botHomeContent); ok && err == nil {
						h.backend = counted
					}
					return disk, err
				}}
			}
			switch d := r.db.(type) {
			case personalBotDB:
				d.content = wrap(d.content)
				r.db = d
			case teamBotDB:
				d.content = wrap(d.content)
				r.db = d
			}
			op, end := WithBotContentReadOperation(ctx)
			defer end()
			read := func(c context.Context) error {
				return r.db.Transaction(c, func(tx botDraftDB) error {
					for range 2 {
						if _, err := tx.ReadVersion(c, b.ID, 1); err != nil {
							return err
						}
					}
					return nil
				})
			}
			for range 5 {
				if err := read(op); err != nil {
					t.Fatal(err)
				}
			}
			if counted.opens != 1 || lookups != 10 {
				t.Fatalf("opens=%d fresh binding lookups=%d", counted.opens, lookups)
			}
			forwarded := WithBotContentReadOperationFromContext(ctx, op)
			if err := read(forwarded); err != nil {
				t.Fatal(err)
			}
			if counted.opens != 1 || lookups != 12 {
				t.Fatalf("forwarded opens=%d fresh binding lookups=%d", counted.opens, lookups)
			}
			end()
			if WithBotContentReadOperationFromContext(ctx, op) != ctx {
				t.Fatal("forwarded closed operation")
			}
			if err := read(op); err != nil {
				t.Fatal(err)
			}
			if counted.opens != 2 {
				t.Fatalf("new transaction after operation close opened %d objects; want 2 total", counted.opens)
			}
			backend.corrupt = true
			if err := read(op); err == nil {
				t.Fatal("ended operation accepted corruption")
			}
			next, closeNext := WithBotContentReadOperation(ctx)
			defer closeNext()
			if err := read(next); err == nil {
				t.Fatal("new operation accepted corruption")
			}
			backend.corrupt = false
			canceled, cancel := context.WithCancel(op)
			cancel()
			if err := read(canceled); err == nil {
				t.Fatal("canceled operation accepted")
			}
		})
	}
}
