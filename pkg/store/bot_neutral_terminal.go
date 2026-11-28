package store

// BotTerminalReason is neutral product status, never model-authored text.
type BotTerminalReason string

const (
	BotTerminalNoAnswer BotTerminalReason = "no_eligible_answer"
)

func (s BotRunState) NeutralTerminalLabel() string {
	if s.TerminalReason != BotTerminalNoAnswer {
		return ""
	}
	switch s.Status {
	case RunPartial:
		return "Partially completed — no Employee answer available"
	case RunFailed:
		return "Failed — no Employee answer available"
	default:
		return ""
	}
}
