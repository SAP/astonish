package store

// BotExecutionIntent is durable single-run consent, not a lifecycle transition.
type BotExecutionIntent string

const (
	BotIntentRegular      BotExecutionIntent = ""
	BotIntentConversation BotExecutionIntent = "conversation"
)

func (i BotExecutionIntent) RequiredStatus() BotStatus {
	switch i {
	case BotIntentRegular:
		return BotActive
	case BotIntentConversation:
		return BotPaused
	default:
		return ""
	}
}

func (i BotExecutionIntent) AllowsStatus(s BotStatus) bool {
	required := i.RequiredStatus()
	return required != "" && s == required
}
