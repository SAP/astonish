package tui

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
)

// Client uncertainty must never manufacture a failed Bot operation or echo wire diagnostics.
const botOutcomeUnknown = "I can’t confirm the latest outcome right now. Please refresh before trying again; your work may still be running."
const botAccessUnavailable = "I can’t access this Bot with your current authorization. Please check your access and refresh. I can’t confirm the outcome of your work."

func botEmployeeFailure(err error) string {
	if status, ok := err.(interface{ StatusCode() int }); ok && (status.StatusCode() == 401 || status.StatusCode() == 403) {
		return botAccessUnavailable
	}
	return botOutcomeUnknown
}

// A local receipt is a completed employee message, not a stream event: it must
// not start/finish a model turn or change the pending authorization state.
func botEmployeeApprovalReceipt(continuation store.BotContinuationState) (events.Item, bool) {
	var text string
	switch continuation {
	case store.BotContinuationBlockedByPause:
		text = "I saved your approval, but I’m still paused."
	case store.BotContinuationQueued:
		text = "I saved your approval and queued the continuation. I haven’t completed the work yet."
	default:
		return events.Item{}, false
	}
	return events.Item{Kind: events.ItemAgent, Content: text}, true
}

// Project only presentation. Keep source error events and state transitions intact.
func botEmployeeItem(item events.Item, selected bool) events.Item {
	if selected && item.Kind == events.ItemError {
		item.Kind = events.ItemAgent
		item.Content = botOutcomeUnknown
	}
	return item
}
