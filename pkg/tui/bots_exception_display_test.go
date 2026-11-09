package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotExceptionDisplaysExactFieldsAndLifetime(t *testing.T) {
	b := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New()}
	op := store.BotOperationView{SchemaVersion: 1, BotID: b.ID, HomeSessionID: b.HomeSessionID, OperationID: uuid.New(), Revision: 1, StateSequence: 1, Operation: store.BotControlMemoryForget, Status: store.BotOperationBlocked, Decision: store.BotOperationRequireAuthorization}
	op.Exception = &store.BotOperationExceptionView{OperationID: op.OperationID, PayloadDigest: "private-digest", ExpiresAt: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), Fields: []store.BotOperationFieldView{{Caption: "Change", Value: "memory_forget"}, {Caption: "Memory", Value: "0714d1d8-d03a-54f4-9153-1b6799e3a8cb"}, {Caption: "Recipient", Value: "owner@example.test"}, {Caption: "Cost limit", Value: "USD 0.25"}}}
	m := model{width: 120, bots: botsState{open: true, exceptions: true, snapshot: &store.BotSnapshot{Bot: b, Operations: []store.BotOperationView{op}}}}
	for _, confirm := range []store.ApprovalStatus{"", store.ApprovalApproved, store.ApprovalDenied} {
		m.bots.confirm = confirm
		text := m.renderBotExceptions()
		for _, want := range []string{"memory forget", "Memory", "0714d1d8-d03a-54f4-9153-1b6799e3a8cb", "Recipient", "owner@example.test", "Cost limit", "USD 0.25", "saved action only", "2099-01-01T00:00:00Z"} {
			if !strings.Contains(text, want) {
				t.Errorf("confirmation %q missing %q: %s", confirm, want, text)
			}
		}
		if strings.Contains(text, "private-digest") {
			t.Fatal("authority digest rendered")
		}
	}
}
