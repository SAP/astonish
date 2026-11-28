package store

// BotExecutionIntent is durable single-run consent, not a lifecycle transition.
type BotExecutionIntent string

const (
	BotIntentRegular      BotExecutionIntent = ""
	BotIntentConversation BotExecutionIntent = "conversation"
	// Minted only by authorized safe-checkpoint continuation, never run admission.
	// It permits the original assignment while recurrence remains paused.
	BotIntentProgressContinuation BotExecutionIntent = "progress_continuation"
)

func (i BotExecutionIntent) RequiredStatus() BotStatus {
	switch i {
	case BotIntentRegular:
		return BotActive
	case BotIntentConversation, BotIntentProgressContinuation:
		return BotPaused
	default:
		return ""
	}
}

func (i BotExecutionIntent) AllowsStatus(s BotStatus) bool {
	required := i.RequiredStatus()
	return required != "" && s == required
}
