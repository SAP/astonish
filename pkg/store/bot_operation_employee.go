package store

// BotOperationEmployeeReport is the employee boundary for direct controls. It
// consumes only a validated operation and the verified schedule projection, never
// an operation's error, receipt summary, user payload prose or model draft. Direct
// controls have no source-turn model budget and therefore never call an LLM.
func BotOperationEmployeeReport(status RunStatus, o BotOperation) (BotReport, error) {
	if o.Validate() != nil {
		return BotReport{}, ErrBotInvalid
	}
	var schedule *BotOperationScheduleView
	if o.Status == BotOperationApplied && o.Receipt != nil && o.Receipt.Schedule != nil {
		s := o.Receipt.Schedule
		if o.Provenance == nil || s.SourceRunID != o.Provenance.SourceRunID || s.UserEventID != o.Provenance.UserEventID || s.SourceDigest != o.Provenance.SourceDigest {
			return BotReport{}, ErrBotInvalid
		}
		schedule = &BotOperationScheduleView{TriggerID: s.TriggerID, ScheduleID: s.ScheduleID, Version: s.Version, Enabled: s.Enabled, Removed: s.Removed, NextRun: s.NextRun}
	}
	report := BotReport{}
	if o.Status == BotOperationApplied {
		switch {
		case isScheduleOperation(o.Payload.Operation) && schedule != nil:
			report.Summary = scheduleOperationSummary(o.Payload, *schedule)
		case o.HasVerifiedMetadataReceipt():
			report.Summary = metadataOperationSummary(o.Payload.Operation)
		default:
			report.Summary = "Requested change applied."
		}
		report.EmployeeMessage = &BotEmployeeMessage{Origin: BotEmployeeMessageOperation, Text: report.Summary, SourceDigest: report.EmployeeMessageSourceDigest(status), OperationID: o.ID, OperationRevision: o.Revision}
	} else {
		switch o.Status {
		case BotOperationPending:
			report.Summary = "Change queued."
		case BotOperationApplying:
			report.Summary = "Applying change."
		case BotOperationBlocked:
			report.Summary = "Change needs authorization."
		case BotOperationCancelled:
			report.Summary = "Change cancelled."
		default:
			report.Summary = "Change could not be completed. No change was verified."
		}
		report.EmployeeMessage = &BotEmployeeMessage{Origin: BotEmployeeMessageOperation, Text: report.Summary, SourceDigest: report.EmployeeMessageSourceDigest(status), OperationID: o.ID, OperationRevision: o.Revision}
	}
	return report, report.ValidateEmployeeMessageIntegrity(status)
}
