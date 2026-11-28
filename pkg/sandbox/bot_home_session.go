package sandbox

import "context"

// BotHomeSessionPreparer resumes only an already registered normal session.
// Callers must authorize the scoped Bot and call before opening a SQL transaction.
type BotHomeSessionPreparer interface {
	PrepareBotHomeSession(context.Context, *BotHomeBinding, string) error
}

// BotHomeSessionBinder is a trusted composition seam. Binding is metadata-only;
// file access lazily acquires this session's ordinary compute. It is not authority.
// The selected ID must come from the scoped Bot/run record, never agent files.
type BotHomeSessionBinder interface {
	BindBotHomeSession(*BotHomeBinding, string) error
}
