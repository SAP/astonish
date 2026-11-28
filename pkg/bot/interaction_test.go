package bot

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestInteractionCheckpointNeverGrantsExecution(t *testing.T) {
	i := &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "q", Payload: json.RawMessage(`{"questionId":"q","kind":"yesno","prompt":"Continue?"}`)}
	run := store.BotRun{ID: uuid.New(), BotID: uuid.New(), SessionID: uuid.New(), Version: 1}
	action := execution.ActionDescriptor{Tool: "ask_user", Effect: execution.EffectRead, Arguments: i.Payload, ConstraintsVerified: true}
	digest, _ := action.Digest()
	a := store.BotApproval{ID: uuid.New(), BotID: run.BotID, RunID: run.ID, Version: 1, Action: action, ActionDigest: digest, Interaction: i}
	c := ApprovalCheckpoint{Format: InteractionCheckpointFormat, ApprovalID: a.ID, BotID: run.BotID, RunID: run.ID, SessionID: run.SessionID, Version: 1, ActionDigest: digest, Fence: 1}
	raw, _ := json.Marshal(c)
	restored, err := DecodeApprovalCheckpoint(raw, run)
	if err != nil || !restored.Matches(a) {
		t.Fatal("restoration failed", err)
	}
	if ApprovalGrant(execution.BotRunIdentity{}, a).ID != uuid.Nil {
		t.Fatal("decision minted action grant")
	}
	c.Format = ApprovalCheckpointFormat
	if c.Matches(a) {
		t.Fatal("decision accepted as exact action")
	}
	a.Interaction = nil
	if restored.Matches(a) {
		t.Fatal("action accepted as decision")
	}
	if HasInteractionOwner(context.Background()) || !HasInteractionOwner(WithInteractionOwner(context.Background())) {
		t.Fatal("invalid owner capability")
	}
}
