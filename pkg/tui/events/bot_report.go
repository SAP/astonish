package events

import (
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"regexp"
)

// Match the canonical publication identity emitted by the store and recognized
// by Studio. Neither headings nor arbitrary bot-report prefixes imply a boundary.
var botReportInvocation = regexp.MustCompile(`^bot-report:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func IsBotReportInvocation(id string) bool { return botReportInvocation.MatchString(id) }

// NewBotReport represents one complete, durable publication, not a streaming
// tool-turn chunk or an artifact report marker. EventID is the persisted row ID.
func NewBotReport(invocationID, eventID, text string) Event {
	return Event{Kind: KindBotReport, InvocationID: invocationID, EventID: eventID, Text: text}
}

func (t *Transcript) appendBotReport(ev Event) {
	if !IsBotReportInvocation(ev.InvocationID) || ev.Text == "" {
		return
	}
	for _, item := range t.Items {
		if IsBotReportInvocation(item.InvocationID) && ((ev.EventID != "" && item.EventID == ev.EventID) || (ev.EventID == "" && item.InvocationID == ev.InvocationID)) {
			return
		}
	}
	// Do not apply Done: an autonomous publication must not finalize another
	// running tool turn, clear its status, or adopt its model routing badge.
	text := ev.Text
	var publication *store.BotFilePublication
	if p := ev.BotFilePublication; p != nil {
		if p.Validate() != nil || p.EventID != ev.EventID || p.Summary != ev.Text {
			return
		}
		cp := *p
		cp.Artifacts = append([]store.BotPublishedArtifact(nil), p.Artifacts...)
		publication = &cp
		for _, a := range p.Artifacts {
			text += fmt.Sprintf("\n\nFile: %s (%d bytes) — %s", a.RelativePath, a.SizeBytes, a.Preview)
		}
		text += "\n\n/bots files → p: published files (preview or save)"
	}
	t.Items = append(t.Items, Item{Kind: ItemAgent, InvocationID: ev.InvocationID, EventID: ev.EventID, Content: text, BotFilePublication: publication})
	t.nextTextReplaces = false
}
