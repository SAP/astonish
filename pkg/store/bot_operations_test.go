package store

import (
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestBotMandateOperationTransitions(t *testing.T) {
	if !BotOperationPending.CanTransitionTo(BotOperationApplying) || !BotOperationApplying.CanTransitionTo(BotOperationApplied) || BotOperationApplied.CanTransitionTo(BotOperationApplying) {
		t.Fatal("transitions")
	}
	r := BotOperationReceipt{Status: BotOperationApplied}
	if r.Validate() == nil {
		t.Fatal("claimed success without postconditions")
	}
	r.ObservedAt = time.Now()
	r.Postconditions = []BotOperationPostcondition{{Resource: "schedule/1", Revision: 1, Verified: true}}
	if r.Validate() != nil {
		t.Fatal("observed receipt rejected")
	}
}
func TestBotMandateOperationCopiesAndValidation(t *testing.T) {
	text := "watch alerts"
	o := BotOperation{ID: uuid.New(), MandateRef: BotMandateRef{ID: uuid.New(), BotID: uuid.New(), OrgSlug: "o", TeamSlug: "t", OwnerID: "u", Revision: 1}, IdempotencyKey: "request", Revision: 1, Status: BotOperationPending, Decision: BotOperationExecute, Payload: BotControlPayload{Operation: BotControlResponsibility, Reason: "owner request", Responsibility: &text}}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	c, err := o.Clone()
	if err != nil {
		t.Fatal(err)
	}
	*c.Payload.Responsibility = "changed"
	if *o.Payload.Responsibility != "watch alerts" {
		t.Fatal("payload alias")
	}
	o.Status = BotOperationApplied
	if o.Validate() == nil {
		t.Fatal("success without receipt")
	}
	o.Status = BotOperationApplying
	o.Decision = BotOperationDeny
	if o.Validate() == nil {
		t.Fatal("denied applying")
	}
}
func TestBotDelegationPolicyDecisions(t *testing.T) {
	for _, d := range []BotOperationPolicyDecision{BotOperationExecute, BotOperationClarify, BotOperationRequireAuthorization, BotOperationDeny} {
		if !d.Valid() {
			t.Fatal(d)
		}
	}
	if BotOperationPolicyDecision("yes").Valid() {
		t.Fatal("unknown policy")
	}
}
