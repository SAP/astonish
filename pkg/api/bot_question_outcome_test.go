package api

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
)

func TestQuestionWaitingEvidenceOnlyPersistedCapture(t *testing.T) {
	report := store.BotReport{Unverified: []string{"Tool ask_user returned without independently verified outcome metadata.", "other unknown failure"}}
	a := &store.BotApproval{ID: uuid.New(), Action: execution.ActionDescriptor{Tool: "ask_user"}, Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "q", Payload: json.RawMessage(`{"questionId":"q","kind":"text","prompt":"Where?"}`)}}
	for _, tc := range []struct {
		capture *botApprovalCapture
		want    int
	}{{nil, 2}, {&botApprovalCapture{}, 2}, {&botApprovalCapture{approval: a, conflict: true}, 2}, {&botApprovalCapture{approval: a}, 1}} {
		got := botQuestionWaitingEvidence(report, tc.capture)
		if len(got.Unverified) != tc.want || got.Unverified[len(got.Unverified)-1] != "other unknown failure" {
			t.Fatalf("evidence=%v", got.Unverified)
		}
	}
}
