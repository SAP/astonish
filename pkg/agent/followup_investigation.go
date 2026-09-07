package agent

import "strings"

// FailedFixFollowupContext is injected into per-turn SessionContext when the
// user's message is a failed-fix follow-up. It is short on purpose: the
// debug-regression skill holds the full protocol.
const FailedFixFollowupContext = `This turn is a failed-fix follow-up. Do not blame the binary, build cache, or environment. Do not reuse a hypothesis the user already denied. Recover git history for the failing surface, then encode the user-visible sequence as a failing test before more production edits. Call skill_lookup("debug-regression") if not already loaded this session.`

// LiveSurfaceFollowupContext is injected when the user is asking about a live
// session/sandbox/browser. It is short on purpose: inspect-live-surface holds
// the full protocol. This applies in Studio and Code — the chromium PATH miss
// happened in Studio, which has no Work Policy of its own.
const LiveSurfaceFollowupContext = `This turn is a live-surface follow-up. Do not treat a missing debian package name on PATH (which chromium, google-chrome) as proof the product capability is missing. Trace the product launch path, inspect the live overlay/session/process, and use the product tool (browser_navigate for the sandbox browser). Call skill_lookup("inspect-live-surface") if not already loaded this session. Do not offer apt-get install chromium as the CloakBrowser fix.`

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
	"doesn't work",
	"does not work",
	"no such object",
	"not launching",
	"still using the host",
	"how do you considered as completed",
	"all phases completed",
	"build failed",
	"undefined:",
}

var liveSurfaceFollowupNeedles = []string{
	"chromium",
	"google-chrome",
	"cloakbrowser",
	"browser_navigate",
	"no running sandbox",
	"cdp is not bound",
	"kasmvnc",
	"which chromium",
	"no chromium",
	"overlayfs",
	"fuse-overlayfs",
	"in the session",
	"in this sandbox",
	"astonish-session",
	"not installed",
}

// IsFailedFixFollowup reports whether cleaned user text looks like a
// failed-fix follow-up (or a first report that the UI still does not match).
func IsFailedFixFollowup(userText string) bool {
	return containsAnyNeedle(userText, failedFixFollowupNeedles)
}

// IsLiveSurfaceFollowup reports whether cleaned user text is about a live
// sandbox/session/browser rather than a code-logic regression.
func IsLiveSurfaceFollowup(userText string) bool {
	return containsAnyNeedle(userText, liveSurfaceFollowupNeedles)
}

func containsAnyNeedle(userText string, needles []string) bool {
	low := strings.ToLower(strings.TrimSpace(userText))
	if low == "" {
		return false
	}
	for _, n := range needles {
		if strings.Contains(low, n) {
			return true
		}
	}
	return false
}

// FollowupInvestigationContext chooses the per-turn injector block.
// Live-surface wins when both match. Code-mode failed-fix still loads
// debug-regression. Studio sandbox "no difference" without live keywords
// still inspects the live surface (that is how the chromium miss presented).
func FollowupInvestigationContext(userText string, codeMode, sandboxEnabled bool) string {
	live := IsLiveSurfaceFollowup(userText)
	failed := IsFailedFixFollowup(userText)
	switch {
	case live:
		return LiveSurfaceFollowupContext
	case failed && codeMode:
		return FailedFixFollowupContext
	case failed && sandboxEnabled && !codeMode:
		return LiveSurfaceFollowupContext
	default:
		return ""
	}
}

// AppendFailedFixFollowupContext appends FailedFixFollowupContext to an
// existing SessionContext (for example Graph Plan mode instructions).
func AppendFailedFixFollowupContext(sessionContext string) string {
	return AppendFollowupContext(sessionContext, FailedFixFollowupContext)
}

// AppendFollowupContext appends block to sessionContext if it is not already
// present. Empty block is a no-op.
func AppendFollowupContext(sessionContext, block string) string {
	block = strings.TrimSpace(block)
	if block == "" {
		return sessionContext
	}
	if strings.Contains(sessionContext, block) {
		return sessionContext
	}
	sessionContext = strings.TrimSpace(sessionContext)
	if sessionContext == "" {
		return block
	}
	return sessionContext + "\n\n" + block
}
