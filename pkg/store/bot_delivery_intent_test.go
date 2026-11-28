package store

import (
	"errors"
	"testing"
)

func TestBotFileDeliveryIntent(t *testing.T) {
	a := &BotAgentWork{}
	if err := a.SetDelivery(BotDeliveryChatOnly, ""); err != nil {
		t.Fatal(err)
	}
	if err := a.SetDelivery(BotDeliveryFileDeliverable, BotDeliverableMarkdown); err != nil {
		t.Fatal(err)
	}
	if err := a.SetDelivery(BotDeliveryChatOnly, ""); !errors.Is(err, ErrBotConflict) {
		t.Fatalf("downgraded: %v", err)
	}
	if err := a.SetDelivery(BotDeliveryFileDeliverable, BotDeliverableBinary); !errors.Is(err, ErrBotConflict) {
		t.Fatalf("changed format: %v", err)
	}
	if err := (&BotAgentWork{}).SetDelivery(BotDeliveryFileDeliverable, "pdf"); !errors.Is(err, ErrBotInvalid) {
		t.Fatalf("invalid format: %v", err)
	}
	w := &BotWorkState{Phase: BotWorkFinished, AgentWork: a, Candidate: &BotWorkCandidate{Text: "pretend saved"}}
	if err := w.Validate(); err == nil {
		t.Fatal("missing file receipt accepted")
	}
}
