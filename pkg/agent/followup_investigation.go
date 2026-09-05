package agent

import "strings"

// FailedFixFollowupContext is injected into per-turn SessionContext when the
// user's message is a failed-fix follow-up. It is short on purpose: the
// debug-regression skill holds the full protocol.
const FailedFixFollowupContext = `This turn is a failed-fix follow-up. Do not blame the binary, build cache, or environment. Do not reuse a hypothesis the user already denied. Recover git history for the failing surface, then encode the user-visible sequence as a failing test before more production edits. Call skill_lookup("debug-regression") if not already loaded this session.`

var failedFixFollowupNeedles = []string{
	"zero difference",
	"no difference",
	"nothing changed",
	"nothing gets fixed",
	"still not",
	"still broken",
	"still doesn't",
	"still does not",
	"doesn't show",
	"does not show",
	"same problem",
	"not the binary",
	"always rebuild",
	"always run make",
	"i always run",
	"used to work",
	"previous fix",
}

// IsFailedFixFollowup reports whether cleaned user text looks like a
// failed-fix follow-up (or a first report that the UI still does not match).
func IsFailedFixFollowup(userText string) bool {
	low := strings.ToLower(strings.TrimSpace(userText))
	if low == "" {
		return false
	}
	for _, n := range failedFixFollowupNeedles {
		if strings.Contains(low, n) {
			return true
		}
	}
	return false
}

// AppendFailedFixFollowupContext appends FailedFixFollowupContext to an
// existing SessionContext (for example Graph Plan mode instructions).
func AppendFailedFixFollowupContext(sessionContext string) string {
	if strings.Contains(sessionContext, FailedFixFollowupContext) {
		return sessionContext
	}
	sessionContext = strings.TrimSpace(sessionContext)
	if sessionContext == "" {
		return FailedFixFollowupContext
	}
	return sessionContext + "\n\n" + FailedFixFollowupContext
}
