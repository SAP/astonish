package entstore

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestRoutineSourceSuppressionFingerprint(t *testing.T) {
	_, s, owner, _, q := authorizedScheduleFixture(t, store.BotScopePersonal)
	m, err := s.ReadBotMandate(owner, q.MandateRef)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := s.policy(context.Background(), q.MandateRef)
	if err != nil {
		t.Fatal(err)
	}
	text := "concise"
	payload := store.BotControlPayload{Operation: store.BotControlResponsibility, Reason: "first", Responsibility: &text}
	source := m.Provenance.SourceDigest
	key := routineSourceSuppressionKey(payload, m.Mandate, policy, source)
	payload.Reason = "model claims fresh consent"
	m.Mandate.Ref.ID = uuid.New()
	if key != routineSourceSuppressionKey(payload, m.Mandate, policy, source) {
		t.Fatal("model narration or mandate ID reopened denial")
	}
	if key == routineSourceSuppressionKey(payload, m.Mandate, policy, "different verified source") {
		t.Fatal("fresh source cannot request new exception")
	}
	rec := &botStoredRecord{Metadata: map[string]any{routineSuppressionKey(payload, m.Mandate, policy): true}}
	denied, err := routineSourceSuppressed(owner, nil, rec, payload, m.Mandate, policy, source)
	if err != nil || !denied {
		t.Fatalf("unexplained legacy marker not fail closed: %v %v", denied, err)
	}
	if _, err = routineSourceSuppressed(owner, nil, rec, payload, m.Mandate, policy, ""); err == nil {
		t.Fatal("empty source accepted")
	}
}
