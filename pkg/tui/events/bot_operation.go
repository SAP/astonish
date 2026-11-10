package events

import (
	"fmt"
	"github.com/SAP/astonish/pkg/store"
)

// Each operation owns its ordering; unrelated operation sequence numbers never compete.
func (t *Transcript) applyBotOperation(v *store.BotOperationView) {
	if v == nil || v.Validate() != nil || v.BotID.String() != t.BotID || v.HomeSessionID.String() != t.SessionID {
		return
	}
	id := v.OperationID.String()
	if t.BotOperations == nil {
		t.BotOperations = map[string]store.BotOperationView{}
	}
	if p, ok := t.BotOperations[id]; ok && (v.Revision <= p.Revision || v.StateSequence <= p.StateSequence) {
		return
	}
	t.BotOperations[id] = *v
	// Presentation only controls transcript items. Internal operations still own
	// their authoritative head and pending/applying status, including on replay.
	switch v.Presentation {
	case store.BotOperationPresentationInternal:
		return
	case store.BotOperationPresentationConversation:
		t.upsertBotOperationItem(id, ItemAgent, v.Summary)
		return
	}
	text := fmt.Sprintf("%s — %s", v.Status, v.Summary)
	if v.Blocker != "" {
		text += "\n" + v.Blocker
	}
	if s := v.VerifiedSchedule; s != nil {
		switch {
		case s.Removed:
			text += "\nSchedule removed."
		case s.Enabled:
			text += "\nSchedule enabled."
		default:
			text += "\nSchedule disabled."
		}
		if s.NextRun != nil {
			text += " Next run: " + s.NextRun.Format("2006-01-02 15:04 MST")
		}
	}
	if v.Status == store.BotOperationBlocked && v.Decision == store.BotOperationRequireAuthorization && v.Exception != nil {
		text += "\nAuthorization exception: /bots exceptions to review."
	}
	t.upsertBotOperationItem(id, ItemSystem, text)
}

// The operation identity is stable across revisions and reconnects. Keep final
// conversational receipts separate from streaming agent text and model badges.
func (t *Transcript) upsertBotOperationItem(id string, kind ItemKind, text string) {
	item := Item{Kind: kind, Content: text, BotOperationID: id}
	for i := range t.Items {
		if t.Items[i].BotOperationID == id {
			t.Items[i] = item
			return
		}
	}
	t.Items = append(t.Items, item)
}

// isBotPublication identifies complete messages that must not participate in
// sticky-agent replacement, tool reordering, or model routing attribution.
func (it Item) isBotPublication() bool {
	return it.BotOperationID != "" || IsBotReportInvocation(it.InvocationID)
}

func (t *Transcript) restoreBotOperationState() {
	if len(t.BotOperations) == 0 {
		return
	}
	pending, applying := false, false
	for _, v := range t.BotOperations {
		pending = pending || v.Status == store.BotOperationPending
		applying = applying || v.Status == store.BotOperationApplying
	}
	if applying {
		t.Streaming = true
		t.Status = "Applying change…"
		return
	}
	if pending {
		t.Streaming = true
		t.Status = "Change queued…"
		return
	}
	// Run states already restored their own activity. Without any, a completed
	// operation must not inherit its previous pending spinner.
	if len(t.BotRunStates) == 0 {
		t.Streaming = false
		t.Status = ""
	}
}
