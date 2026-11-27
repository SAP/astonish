package tui

import (
	"errors"

	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
)

// Product feedback is never Employee speech. Keep transport diagnostics private
// without inventing an operation failure or claiming that a request completed.
const botOutcomeUnknown = "Latest outcome unavailable. Refresh before trying again; work may still be running."
const botAccessUnavailable = "Bot access unavailable with the current authorization. Check access and refresh. The work outcome is unconfirmed."

func botEmployeeFailure(err error) string {
	var status interface{ StatusCode() int }
	if errors.As(err, &status) && (status.StatusCode() == 401 || status.StatusCode() == 403) {
		return botAccessUnavailable
	}
	return botOutcomeUnknown
}

// Local approval receipts are neutral product status, not a model turn. They
// neither start/finish the stream nor alter pending authorization state.
func botEmployeeApprovalReceipt(continuation store.BotContinuationState) (events.Item, bool) {
	var text string
	switch continuation {
	case store.BotContinuationBlockedByPause:
		text = "Approval saved. Bot remains paused."
	case store.BotContinuationQueued:
		text = "Approval saved. Continuation queued; work is not completed."
	default:
		return events.Item{}, false
	}
	return events.Item{Kind: events.ItemSystem, Content: text}, true
}

// Project only Bot presentation. Source diagnostics remain available privately;
// ordinary Chat and Code error rendering is unchanged.
func botEmployeeItem(item events.Item, selected bool) events.Item {
	if selected && item.Kind == events.ItemError {
		item.Kind = events.ItemSystem
		item.Content = botOutcomeUnknown
	}
	return item
}
