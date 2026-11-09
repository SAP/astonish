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
