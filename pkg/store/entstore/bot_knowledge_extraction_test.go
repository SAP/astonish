package entstore

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func knowledgeStorageFixture(t *testing.T, scope store.BotScope) (*botRepository, context.Context, store.BotKnowledgeExtractionInput, *time.Time) {
	t.Helper()
	r, owner, worker, run, lease, now := botRunFixture(t, scope)
	report := finishedAgentWorkReport(t, r, worker, lease, "Source unavailable", store.AgentWorkBlocked)
	report.Unverified = []string{"source unavailable"}
	if e := r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunPartial, Report: report}); e != nil {
		t.Fatal(e)
	}
	saved, e := r.db.ReadRun(owner, run.BotID, run.ID)
	if e != nil {
		t.Fatal(e)
	}
	a, e := r.db.FindActivity(owner, run.BotID, "result:"+run.ID.String()+":"+knowledgeFenceString(lease.Fence))
	if e != nil {
		t.Fatal(e)
	}
	outcome := store.BotKnowledgeOutcome{Status: string(saved.Run.Status), Answer: saved.Run.Report.Summary}
	input := store.BotKnowledgeExtractionInput{SchemaVersion: 1, Complete: true, OwnerMessage: saved.Instructions, Outcome: outcome, Source: store.BotKnowledgeExtractionSource{BotID: run.BotID, RunID: run.ID, Version: run.Version, Sequence: a.Sequence, OutcomeDigest: store.BotKnowledgeDigest(outcome)}}
	owner = WithBotKnowledgeSourceVerifier(owner, func(_ context.Context, candidate store.BotKnowledgeExtractionInput) error {
		if candidate.Source != input.Source || candidate.Conversation != "" || len(candidate.Outcome.Tools) != 0 || len(candidate.PriorFiles) != 0 {
			return store.ErrBotForbidden
		}
		return nil
	})
	return r, owner, input, now
}
func TestBotKnowledgeUsageReservationDurable(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, c, input, _ := knowledgeStorageFixture(t, scope)
			record, e := r.EnqueueBotKnowledgeExtraction(c, input)
			if e != nil {
				t.Fatal(e)
			}
			lease, e := r.ClaimBotKnowledgeExtraction(c, input.Source, "usage", time.Minute)
			if e != nil {
				t.Fatal(e)
			}
			if _, _, e := r.BeginBotKnowledgeExtraction(c, lease, record.Revision); e != nil {
				t.Fatal(e)
			}
			baseline, e := r.GetRun(c, input.Source.BotID, input.Source.RunID)
			if e != nil {
				t.Fatal(e)
			}
			for _, u := range []store.BotUsage{{InputTokens: 1000, OutputTokens: 2000}, {InputTokens: 10, OutputTokens: 20}} {
				if e := RecordBotKnowledgeUsage(c, r, input.Source, lease, u); e != nil {
					t.Fatal(e)
				}
				got, e := r.GetRun(c, input.Source.BotID, input.Source.RunID)
				if e != nil {
					t.Fatal(e)
				}
				if got.Usage.InputTokens != baseline.Usage.InputTokens+u.InputTokens || got.Usage.OutputTokens != baseline.Usage.OutputTokens+u.OutputTokens || got.Lease != baseline.Lease {
					t.Fatalf("usage or lease drift: %+v", got)
				}
			}
		})
	}
}

func TestBotKnowledgeExtractionStorage(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, c, input, now := knowledgeStorageFixture(t, scope)
			*now = now.Add(123 * time.Nanosecond)
			record, e := r.EnqueueBotKnowledgeExtraction(c, input)
			if e != nil {
				t.Fatal(e)
			}
			envelope, e := r.db.(botKnowledgeDB).readKnowledge(c, input.Source.BotID, record.ID)
			if e != nil {
				t.Fatal(e)
			}
			duplicate := *envelope
			duplicate.Record.ID = uuid.New()
			if e = r.db.Transaction(c, func(tx botDraftDB) error {
				return tx.(botKnowledgeDB).saveKnowledge(c, duplicate, 0)
			}); !errors.Is(e, store.ErrBotConflict) {
				t.Fatalf("database source uniqueness missing: %v", e)
			}
			again, e := r.EnqueueBotKnowledgeExtraction(c, input)
			if e != nil || again.ID != record.ID {
				t.Fatalf("dedup %+v %v", again, e)
			}
			changed := input
			changed.Conversation = "forged"
			if _, e = r.EnqueueBotKnowledgeExtraction(c, changed); !errors.Is(e, store.ErrBotConflict) {
				t.Fatalf("mutated source %v", e)
			}
			changed = input
			changed.Source.Sequence++
			if _, e = r.EnqueueBotKnowledgeExtraction(c, changed); !errors.Is(e, store.ErrBotConflict) {
				t.Fatalf("forged sequence %v", e)
			}
			l, e := r.ClaimBotKnowledgeExtraction(c, input.Source, "extractor", time.Minute)
			if e != nil {
				t.Fatal(e)
			}
			if l.ExpiresAt.Nanosecond()%1000 != 0 {
				t.Fatalf("lease timestamp loses PostgreSQL precision: %v", l.ExpiresAt)
			}
			if _, e = r.ClaimBotKnowledgeExtraction(c, input.Source, "other", time.Minute); !errors.Is(e, store.ErrBotConflict) {
				t.Fatalf("concurrent claim %v", e)
			}
			begun, started, e := r.BeginBotKnowledgeExtraction(c, l, record.Revision)
			if e != nil || !started || begun.AttemptID == uuid.Nil {
				t.Fatalf("begin %+v %v", begun, e)
			}
			replay, started, e := r.BeginBotKnowledgeExtraction(c, l, record.Revision)
			if e != nil || started || replay.AttemptID != begun.AttemptID {
				t.Fatalf("begin replay %+v %v", replay, e)
			}
			d := store.BotKnowledgeExtractionDecision{SchemaVersion: 1, Kind: store.BotKnowledgeDecisionNoChange, Reason: "Nothing new"}
			done, e := r.TransitionBotKnowledgeExtraction(c, store.BotKnowledgeExtractionTransition{Lease: l, ExpectedRevision: begun.Revision, From: begun.State, To: store.BotKnowledgeExtractionNoChange, Decision: &d, DecisionDigest: store.BotKnowledgeDigest(d)})
			if e != nil || done.State != store.BotKnowledgeExtractionNoChange {
				t.Fatalf("no change %+v %v", done, e)
			}
			if _, e = r.ClaimBotKnowledgeExtraction(c, input.Source, "other", time.Minute); !errors.Is(e, store.ErrBotConflict) {
				t.Fatalf("terminal reclaimed %v", e)
			}
			foreign := input.Source
			foreign.BotID = uuid.New()
			if _, e = r.ReadBotKnowledgeExtraction(c, foreign); e == nil {
				t.Fatal("foreign Bot read")
			}
		})
	}
}
func TestBotKnowledgeExtractionUncertainRecovery(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, c, input, now := knowledgeStorageFixture(t, scope)
			record, e := r.EnqueueBotKnowledgeExtraction(c, input)
			if e != nil {
				t.Fatal(e)
			}
			l, e := r.ClaimBotKnowledgeExtraction(c, input.Source, "extractor", time.Minute)
			if e != nil {
				t.Fatal(e)
			}
			begun, _, e := r.BeginBotKnowledgeExtraction(c, l, record.Revision)
			if e != nil {
				t.Fatal(e)
			}
			*now = now.Add(2 * time.Minute)
			if _, e = r.ClaimBotKnowledgeExtraction(c, input.Source, "recovery", time.Minute); !errors.Is(e, store.ErrBotConflict) {
				t.Fatalf("uncertain recovered %v", e)
			}
			held, e := r.ReadBotKnowledgeExtraction(c, input.Source)
			if e != nil || held.State != store.BotKnowledgeExtractionHeld || held.AttemptID != begun.AttemptID {
				t.Fatalf("held %+v %v", held, e)
			}
			l, e = r.ClaimBotKnowledgeExtraction(c, input.Source, "recovery", time.Minute)
			if e != nil {
				t.Fatal(e)
			}
			again, started, e := r.BeginBotKnowledgeExtraction(c, l, held.Revision)
			if e != nil || started || again.State != store.BotKnowledgeExtractionHeld {
				t.Fatalf("uncertain model replay %+v %v", again, e)
			}
			if _, e = r.TransitionBotKnowledgeExtraction(c, store.BotKnowledgeExtractionTransition{Lease: l, ExpectedRevision: held.Revision, From: held.State, To: store.BotKnowledgeExtractionPrepared}); e == nil {
				t.Fatal("uncertain retry")
			}
			if _, e = r.TransitionBotKnowledgeExtraction(c, store.BotKnowledgeExtractionTransition{Lease: l, ExpectedRevision: held.Revision, From: held.State, To: store.BotKnowledgeExtractionAborted}); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestBotKnowledgeExtractionPublicationCAS(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, c, input, now := knowledgeStorageFixture(t, scope)
			record, e := r.EnqueueBotKnowledgeExtraction(c, input)
			if e != nil {
				t.Fatal(e)
			}
			l, e := r.ClaimBotKnowledgeExtraction(c, input.Source, "extractor", time.Minute)
			if e != nil {
				t.Fatal(e)
			}
			begun, _, e := r.BeginBotKnowledgeExtraction(c, l, record.Revision)
			if e != nil {
				t.Fatal(e)
			}
			decision := store.BotKnowledgeExtractionDecision{SchemaVersion: 1, Kind: store.BotKnowledgeDecisionChanges, Reason: "Honest limitation", Proposals: []store.BotKnowledgeProposal{{Kind: store.BotKnowledgeProposalFailure, Path: "failures/source.md", Content: "# Source\nUnavailable."}}}
			prepared, e := r.TransitionBotKnowledgeExtraction(c, store.BotKnowledgeExtractionTransition{Lease: l, ExpectedRevision: begun.Revision, From: begun.State, To: store.BotKnowledgeExtractionPrepared, Decision: &decision, DecisionDigest: store.BotKnowledgeDigest(decision)})
			if e != nil {
				t.Fatal(e)
			}
			*now = now.Add(2 * time.Minute)
			fresh, e := r.ClaimBotKnowledgeExtraction(c, input.Source, "publisher", time.Minute)
			if e != nil || fresh.Fence <= l.Fence {
				t.Fatalf("fresh %+v %v", fresh, e)
			}
			q := store.BotKnowledgeExtractionTransition{Lease: fresh, ExpectedRevision: prepared.Revision, From: prepared.State, To: store.BotKnowledgeExtractionFilesReady, Readback: []store.BotKnowledgeFileReadback{{Path: "failures/source.md", Digest: skillSHA(decision.Proposals[0].Content)}}}
			if _, e = r.TransitionBotKnowledgeExtraction(c, q); !errors.Is(e, store.ErrBotUnsupported) {
				t.Fatalf("forged readback %v", e)
			}
			trusted := WithBotKnowledgePublicationVerifier(c, func(context.Context, store.BotKnowledgeExtractionLease, store.BotKnowledgeExtraction, []store.BotKnowledgeFileReadback) error {
				return nil
			})
			ready, e := r.TransitionBotKnowledgeExtraction(trusted, q)
			if e != nil {
				t.Fatal(e)
			}
			q.ExpectedRevision = ready.Revision
			q.From = ready.State
			q.To = store.BotKnowledgeExtractionCommitted
			committed, e := r.TransitionBotKnowledgeExtraction(trusted, q)
			if e != nil || committed.State != store.BotKnowledgeExtractionCommitted {
				t.Fatalf("commit %+v %v", committed, e)
			}
			if e = r.db.Transaction(c, func(tx botDraftDB) error { return tx.(botKnowledgeDB).commitKnowledgeHeads(c, *prepared) }); !errors.Is(e, store.ErrBotConflict) {
				t.Fatalf("overwrite without expected head %v", e)
			}
		})
	}
}
func TestBotKnowledgeExtractionAuthorityCancellationPurge(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, c, input, _ := knowledgeStorageFixture(t, scope)
			record, e := r.EnqueueBotKnowledgeExtraction(c, input)
			if e != nil {
				t.Fatal(e)
			}
			l, e := r.ClaimBotKnowledgeExtraction(c, input.Source, "extractor", time.Minute)
			if e != nil {
				t.Fatal(e)
			}
			old := r.auth.check
			r.auth.check = func(context.Context, string) (string, error) { return "viewer", nil }
			if _, _, e = r.BeginBotKnowledgeExtraction(c, l, record.Revision); !errors.Is(e, store.ErrBotForbidden) {
				t.Fatalf("revoked %v", e)
			}
			r.auth.check = old
			cancelled, cancel := context.WithCancel(c)
			cancel()
			if _, _, e = r.BeginBotKnowledgeExtraction(cancelled, l, record.Revision); !errors.Is(e, context.Canceled) {
				t.Fatalf("cancelled %v", e)
			}
			if e = r.db.Transaction(c, func(tx botDraftDB) error {
				switch d := tx.(type) {
				case personalBotDB:
					return d.purgeKnowledge(c, input.Source.BotID)
				case teamBotDB:
					return d.purgeKnowledge(c, input.Source.BotID)
				}
				return store.ErrBotUnsupported
			}); e != nil {
				t.Fatal(e)
			}
			if _, e = r.ReadBotKnowledgeExtraction(c, input.Source); !errors.Is(e, store.ErrBotNotFound) {
				t.Fatalf("purge %v", e)
			}
		})
	}
}
