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
	for i := range t.Items {
		if t.Items[i].BotOperationID == id {
			t.Items[i].Content = text
			return
		}
	}
	t.Items = append(t.Items, Item{Kind: ItemSystem, Content: text, BotOperationID: id})
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
