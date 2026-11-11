package store

import (
	"encoding/json"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestBotRoutineOperationPresentation(t *testing.T) {
	b := Bot{ID: uuid.New(), HomeSessionID: uuid.New()}
	ref := BotMandateRef{ID: uuid.New(), BotID: b.ID, OrgSlug: "org", TeamSlug: "team", OwnerID: uuid.NewString(), Revision: 1}
	text := "private responsibility"
	for _, kind := range []BotControlOperation{BotControlResponsibility, BotControlPersona, BotControlGoalCreate, BotControlGoalEdit, BotControlMemoryRemember, BotControlMemoryCorrect, BotControlMemoryForget} {
		for _, status := range []BotOperationStatus{BotOperationPending, BotOperationApplying, BotOperationApplied, BotOperationFailed, BotOperationBlocked, BotOperationCancelled} {
			t.Run(string(kind)+"/"+string(status), func(t *testing.T) {
				payload := BotControlPayload{Operation: kind, Reason: "private reason"}
				switch kind {
				case BotControlResponsibility:
					payload.Responsibility = &text
				case BotControlPersona:
					payload.Persona = &BotPersona{}
				case BotControlGoalCreate, BotControlGoalEdit:
					payload.Goal = &BotControlGoal{Goal: BotGoal{ID: uuid.New(), BotID: b.ID, Title: "private goal", CompletionCriteria: "private criteria"}}
				case BotControlMemoryRemember, BotControlMemoryCorrect, BotControlMemoryForget:
					payload.Memory = &BotControlMemory{ExpectedRevision: 1, MemoryID: uuid.NewString(), ExpectedFingerprint: "private fingerprint", Content: text}
				}
				op := BotOperation{ID: uuid.New(), MandateRef: ref, IdempotencyKey: "private-key", Revision: 1, Status: status, Decision: BotOperationExecute, Payload: payload, Provenance: &BotOperationProvenance{SourceRunID: uuid.New(), UserEventID: uuid.NewString()}}
				if status == BotOperationApplied {
					op.Receipt = &BotOperationReceipt{Status: status, ObservedAt: time.Now(), Postconditions: []BotOperationPostcondition{{Resource: "bot_control:" + string(kind), Revision: 1, Verified: true}}}
				}
				view, err := ProjectBotOperation(b, op, 10)
				if err != nil {
					t.Fatal(err)
				}
				want := BotOperationPresentationConversation
				if status == BotOperationPending || status == BotOperationApplying {
					want = BotOperationPresentationInternal
				}
				if view.Presentation != want {
					t.Fatalf("presentation: %+v", view)
				}
				raw, _ := json.Marshal(view)
				if strings.Contains(string(raw), "private") || strings.Contains(view.Summary, op.ID.String()) {
					t.Fatalf("private prose: %s", raw)
				}
				if status == BotOperationApplied {
					op.Receipt.Postconditions[0].Resource = "unrelated"
					fallback, err := ProjectBotOperation(b, op, 11)
					if err != nil || fallback.Presentation != "" {
						t.Fatalf("unrelated receipt promoted: %+v %v", fallback, err)
					}
				}
				if status == BotOperationBlocked {
					op.Decision = BotOperationRequireAuthorization
					fallback, err := ProjectBotOperation(b, op, 11)
					if err != nil || fallback.Presentation != "" {
						t.Fatalf("authorization hidden: %+v %v", fallback, err)
					}
				}
			})
		}
	}
	for _, kind := range []BotControlOperation{BotControlResumeTasks, BotControlTaskOnceOperation, BotControlPriorityPause, BotControlResume} {
		if BotControlConversationalMetadata(kind) {
			t.Fatalf("unsafe lifecycle promotion: %s", kind)
		}
	}
}
