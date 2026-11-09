package api

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Only a persisted interaction capture explains this specific missing receipt.
// Other tools' uncertainty and all unknown errors remain diagnostic evidence.
func botQuestionWaitingEvidence(report store.BotReport, capture *botApprovalCapture) store.BotReport {
	if capture == nil {
		return report
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	a := capture.approval
	if capture.conflict || a == nil || a.ID == uuid.Nil || a.Interaction == nil || a.Interaction.Validate() != nil || a.Action.Tool != "ask_user" {
		return report
	}
	filtered := make([]string, 0, len(report.Unverified))
	for _, item := range report.Unverified {
		if item != "Tool ask_user returned without independently verified outcome metadata." {
			filtered = append(filtered, item)
		}
	}
	report.Unverified = filtered
	return report
}
