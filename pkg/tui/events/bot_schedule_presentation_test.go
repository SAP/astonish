package events

import (
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func schedulePresentationView() store.BotOperationView {
	return store.BotOperationView{SchemaVersion: 1, BotID: uuid.New(), HomeSessionID: uuid.New(), OperationID: uuid.New(), Revision: 1, StateSequence: 10, Operation: store.BotControlScheduleCreate, Status: store.BotOperationPending, Decision: store.BotOperationExecute, Presentation: store.BotOperationPresentationInternal, Summary: "Change queued."}
}

func TestBotSchedulePresentationOrdering(t *testing.T) {
	v := schedulePresentationView()
	tr := NewTranscript()
	tr.BotID, tr.SessionID = v.BotID.String(), v.HomeSessionID.String()
	apply := func(v store.BotOperationView) {
		t.Helper()
		if err := v.Validate(); err != nil {
			t.Fatal(err)
		}
		tr.Apply(Event{Kind: KindBotOperation, BotOperation: &v})
	}
	pending := v
	apply(v)
	tr.Apply(NewDone())
	if len(tr.Items) != 0 || !tr.Streaming || tr.Status != "Change queued…" || tr.BotOperations[v.OperationID.String()].Revision != 1 {
		t.Fatalf("internal pending lost state or leaked: %+v", tr)
	}
	v.Revision++
	v.StateSequence++
	v.Status = store.BotOperationApplying
	v.Summary = "Applying change."
	apply(v)
	if len(tr.Items) != 0 || !tr.Streaming || tr.Status != "Applying change…" {
		t.Fatalf("internal applying leaked: %+v", tr)
	}
	v.Revision++
	v.StateSequence++
	v.Status = store.BotOperationApplied
	v.Presentation = store.BotOperationPresentationConversation
	v.Summary = "I've created the schedule. It runs every minute."
	v.VerifiedSchedule = &store.BotOperationScheduleView{Enabled: true, Version: 1}
	apply(v)
	apply(v)
	apply(pending)
	// Neither a newer revision with stale sequence nor the reverse may win.
	pending.Revision = v.Revision + 1
	apply(pending)
	pending.Revision = 1
	pending.StateSequence = v.StateSequence + 1
	apply(pending)
	if tr.Streaming || tr.Status != "" || len(tr.Items) != 1 {
		t.Fatalf("final duplicated or regressed: %+v", tr)
	}
	item := tr.Items[0]
	if item.Kind != ItemAgent || item.Content != v.Summary || item.BotOperationID != v.OperationID.String() || item.Provisional {
		t.Fatalf("not one stable assistant confirmation: %+v", item)
	}
	if tr.BotOperations[v.OperationID.String()].Status != store.BotOperationApplied {
		t.Fatal("head regressed")
	}
	// A new authoritative revision updates the same conversation item.
	v.Revision++
	v.StateSequence++
	v.Summary = "I've updated the schedule."
	apply(v)
	if len(tr.Items) != 1 || tr.Items[0].Content != v.Summary {
		t.Fatal("new revision duplicated confirmation")
	}
}

func TestBotScheduleConversationBoundary(t *testing.T) {
	for _, linear := range []bool{false, true} {
		t.Run(map[bool]string{false: "sticky", true: "linear"}[linear], func(t *testing.T) {
			v := schedulePresentationView()
			v.Status, v.Presentation = store.BotOperationFailed, store.BotOperationPresentationConversation
			v.Summary = "I couldn't complete the schedule change. No schedule change was verified."
			tr := NewTranscript()
			tr.LinearThread = linear
			tr.BotID, tr.SessionID = v.BotID.String(), v.HomeSessionID.String()
			tr.Apply(Event{Kind: KindBotOperation, BotOperation: &v})
			tr.Apply(makeRoutingInfo("unrelated-model", "weak", 1))
			if tr.Items[0].RoutingModel != "" {
				t.Fatal("final confirmation inherited routing attribution")
			}
			tr.Apply(NewText("Separate response."))
			tr.Apply(NewToolCall("lookup", "lookup-1", nil))
			tr.Apply(NewToolResult("lookup", "lookup-1", "ok"))
			tr.Apply(NewText("Another response."))
			tr.Apply(makeRoutingInfo("other-model", "weak", 1))
			tr.Apply(NewDone())
			if tr.Items[0].Content != v.Summary || tr.Items[0].Kind != ItemAgent || tr.Items[0].Provisional || tr.Items[0].RoutingModel != "" {
				t.Fatalf("confirmation mutated by later stream: %+v", tr.Items)
			}
		})
	}
}

func TestBotScheduleLegacyAndAuthorizationPresentation(t *testing.T) {
	for _, status := range []store.BotOperationStatus{store.BotOperationPending, store.BotOperationBlocked} {
		v := schedulePresentationView()
		v.Presentation = ""
		v.Status = status
		if status == store.BotOperationBlocked {
			v.Decision = store.BotOperationRequireAuthorization
			v.Exception = &store.BotOperationExceptionView{OperationID: v.OperationID}
		}
		tr := NewTranscript()
		tr.BotID, tr.SessionID = v.BotID.String(), v.HomeSessionID.String()
		tr.Apply(Event{Kind: KindBotOperation, BotOperation: &v})
		if len(tr.Items) != 1 || tr.Items[0].Kind != ItemSystem {
			t.Fatalf("legacy changed: %+v", tr.Items)
		}
		if status == store.BotOperationBlocked && tr.Items[0].Content != "blocked — Change queued.\nAuthorization exception: /bots exceptions to review." {
			t.Fatalf("authorization instructions changed: %+v", tr.Items)
		}
	}
}
