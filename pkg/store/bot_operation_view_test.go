package store

import (
	"encoding/json"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestBotOperationViewSafeProjection(t *testing.T) {
	secret := "secret-payload"
	b := Bot{ID: uuid.New(), HomeSessionID: uuid.New()}
	o := BotOperation{ID: uuid.New(), MandateRef: BotMandateRef{ID: uuid.New(), BotID: b.ID, OrgSlug: "org", TeamSlug: "team", OwnerID: "secret-owner", Revision: 1}, IdempotencyKey: "secret-key", Revision: 1, Status: BotOperationPending, Decision: BotOperationExecute, Payload: BotControlPayload{Operation: BotControlResponsibility, Reason: "secret-reason", Responsibility: &secret}}
	for _, status := range []BotOperationStatus{BotOperationPending, BotOperationApplying, BotOperationApplied, BotOperationBlocked, BotOperationFailed, BotOperationCancelled} {
		o.Status = status
		o.Receipt = nil
		if status == BotOperationApplied {
			o.Receipt = &BotOperationReceipt{Status: status, ObservedAt: time.Now(), Postconditions: []BotOperationPostcondition{{Resource: "secret-resource", Revision: 1, Verified: true}}}
		}
		v, err := ProjectBotOperation(b, o, 5)
		if err != nil {
			t.Fatal(status, err)
		}
		raw, _ := json.Marshal(v)
		if strings.Contains(string(raw), "secret") {
			t.Fatal(string(raw))
		}
		a, _ := NewBotOperationEvent(v, time.Now())
		c, _ := NewBotOperationEvent(v, time.Now())
		if a.ID != c.ID {
			t.Fatal("unstable event id")
		}
		v.SchemaVersion = 2
		if v.Validate() == nil {
			t.Fatal("unknown version accepted")
		}
	}
	o.MandateRef.BotID = uuid.New()
	if _, err := ProjectBotOperation(b, o, 5); err == nil {
		t.Fatal("cross bot projection")
	}
}
