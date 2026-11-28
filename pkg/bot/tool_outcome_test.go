package bot

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestToolOutcomeOwnerContext(t *testing.T) {
	type viewerKey struct{}
	src, _ := WithToolOutcomes(context.WithValue(context.Background(), viewerKey{}, "viewer"))
	dst := WithToolOutcomesFromContext(context.Background(), src)
	if dst.Value(viewerKey{}) != nil {
		t.Fatal("ambient viewer context leaked")
	}
	RecordToolOutcome(dst, "call", ToolOutcome{Verified: true})
	if o, ok := TakeToolOutcome(src, "call"); !ok || !o.Verified {
		t.Fatal("owner tool ledger disconnected")
	}
	empty := WithToolOutcomesFromContext(context.Background(), context.Background())
	RecordToolOutcome(empty, "call", ToolOutcome{Verified: true})
	if _, ok := TakeToolOutcome(empty, "call"); ok {
		t.Fatal("missing ledger created implicitly")
	}
}

func TestToolNonexecutionLedger(t *testing.T) {
	ctx, _ := WithToolOutcomes(context.Background())
	receipt := &ToolNonexecutionReceipt{CallID: "rejected", ToolName: "bot_work_update"}
	RecordToolOutcome(ctx, receipt.CallID, ToolOutcome{Verified: true, Nonexecution: receipt})
	receipt.ToolName = "spoofed"
	outcome, ok := TakeToolOutcome(ctx, "rejected")
	if !ok || outcome.Nonexecution == nil || outcome.Nonexecution.ToolName != "bot_work_update" {
		t.Fatalf("receipt was not snapshotted: %+v", outcome)
	}
	if _, ok := TakeToolOutcome(ctx, "rejected"); ok {
		t.Fatal("receipt replayed")
	}
	for _, reverse := range []bool{false, true} {
		failed := ToolOutcome{Unverified: []string{"send receipt missing"}}
		rejected := ToolOutcome{Verified: true, Nonexecution: &ToolNonexecutionReceipt{CallID: "conflict", ToolName: "send"}}
		if reverse {
			failed, rejected = rejected, failed
		}
		RecordToolOutcome(ctx, "conflict", failed)
		RecordToolOutcome(ctx, "conflict", rejected)
		got, ok := TakeToolOutcome(ctx, "conflict")
		if !ok || !got.ReceiptConflict {
			t.Fatalf("duplicate record erased conflict: %+v", got)
		}
	}
}

func TestToolOutcomeLedger(t *testing.T) {
	ctx, _ := WithToolOutcomes(context.Background())
	e := store.BotEvidence{Kind: store.BotEvidenceObservation, Summary: "observed", Source: "fixture", ObservedAt: time.Now(), ActorID: uuid.New()}
	if !ValidObservation(e) {
		t.Fatal("valid observation rejected")
	}
	e.Kind = store.BotEvidenceInference
	if ValidObservation(e) {
		t.Fatal("inference trusted")
	}
	RecordToolOutcome(ctx, "call", ToolOutcome{Verified: true})
	if _, ok := TakeToolOutcome(ctx, "other"); ok {
		t.Fatal("wrong call consumed evidence")
	}
	if o, ok := TakeToolOutcome(ctx, "call"); !ok || !o.Verified {
		t.Fatal("missing evidence")
	}
	if _, ok := TakeToolOutcome(ctx, "call"); ok {
		t.Fatal("evidence replay")
	}
	if _, ok := TakeToolOutcome(context.Background(), "call"); ok {
		t.Fatal("cross-run evidence")
	}
}
