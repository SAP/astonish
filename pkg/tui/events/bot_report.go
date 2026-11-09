package events

import "regexp"

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
	t.Items = append(t.Items, Item{Kind: ItemAgent, InvocationID: ev.InvocationID, EventID: ev.EventID, Content: ev.Text})
	t.nextTextReplaces = false
}
