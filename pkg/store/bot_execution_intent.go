package store

// BotExecutionIntent is durable, single-run consent, not a lifecycle transition.
// The zero value is regular for pre-intent rows and existing callers.
type BotExecutionIntent string

const (
	BotIntentDraftConversation BotExecutionIntent = "draft_conversation"
	BotIntentRegular           BotExecutionIntent = ""
	BotIntentDraftTest         BotExecutionIntent = "draft_test"
	BotIntentConversation      BotExecutionIntent = "conversation"
)

func (i BotExecutionIntent) RequiredStatus() BotStatus {
	switch i {
	case BotIntentRegular:
		return BotActive
	case BotIntentDraftTest, BotIntentDraftConversation:
		return BotDraft
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
