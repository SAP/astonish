package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
)

func TestBotEmployeeApprovalReceipt(t *testing.T) {
	for _, continuation := range []store.BotContinuationState{store.BotContinuationBlockedByPause, store.BotContinuationQueued} {
		t.Run(string(continuation), func(t *testing.T) {
			b := &testBotBackend{selected: &store.Bot{ID: uuid.New()}}
			m := newModel(context.Background(), Config{Backend: b})
			m.width, m.height = 100, 30
			m.bots.open = true
			m.bots.requestID = uuid.New()
			m.tr.Apply(events.NewApproval("exact-tool", map[string]any{"scope": "exact"}, []string{"Allow", "Deny"}))
			streaming := m.tr.Streaming
			count := len(m.tr.Items)
			updated, _ := m.Update(botDecisionMsg{continuation: continuation, requestID: m.bots.requestID, generation: m.botGeneration})
			got := updated.(model)
			if len(got.tr.Items) != count+1 {
				t.Fatalf("receipt count: %d", len(got.tr.Items))
			}
			item := got.tr.Items[count]
			want, _ := botEmployeeApprovalReceipt(continuation)
			if item.Kind != events.ItemAgent || item.Content != want.Content || item.Provisional {
				t.Fatalf("not a completed employee receipt: %+v", item)
			}
			if got.tr.Streaming != streaming || got.tr.Items[count-1].Kind != events.ItemApproval {
				t.Fatal("receipt changed stream or authorization state")
			}
			rendered, _, _ := got.renderTranscript()
			if !strings.Contains(rendered, "I saved your approval") || strings.Contains(rendered, "Approval saved;") {
				t.Fatalf("wrong receipt rendering: %s", rendered)
			}
		})
	}
	if _, ok := botEmployeeApprovalReceipt(store.BotContinuationState("unknown")); ok {
		t.Fatal("invented receipt for unknown state")
	}
}

func TestBotReceiptDoesNotRewriteOrdinaryChatSystemNotice(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: &testBotBackend{}})
	m.applyEvent(events.NewSystem("Approval saved; continuation queued."))
	item := m.tr.Items[len(m.tr.Items)-1]
	projected := botEmployeeItem(item, false)
	if item.Kind != events.ItemSystem || projected.Kind != item.Kind || projected.Content != item.Content {
		t.Fatal("ordinary Chat notice changed")
	}
}
