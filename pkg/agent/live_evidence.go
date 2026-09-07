package agent

import "strings"

// LiveEvidenceSection is the always-on verification block shared by Studio and
// Code. Keep it short: Studio's static prompt has a hard size budget. Protocol
// details live in inspect-live-surface / verify-live-with-drill /
// watch-long-running. codeMode adds the stop-exploring exception.
func LiveEvidenceSection(codeMode bool) string {
	var sb strings.Builder
	sb.WriteString("## Live Evidence\n\n")
	sb.WriteString("- Keep every explicit user requirement until it is completed, superseded, or blocked out loud.\n")
	sb.WriteString("- Claim done only when this turn's tool output supports it. Running surfaces (sandbox, browser, daemon, CLI, UI): live observation or a drill — not unrelated `go test`. Lead with what is true; do not open with \"No, X is not installed\" after `which chromium`.\n")
	sb.WriteString("- Sandbox browser is CloakBrowser at `/home/browser/.cloakbrowser/*/chrome`, not on PATH. Use `browser_navigate`. Starting a rebuild is not done: exit 0 AND the artifact (`process_read`).\n")
	if codeMode {
		sb.WriteString("- Stop-exploring applies to code edits. Running-surface bugs: inspect the live process/overlay before more production edits.\n")
	}
	return sb.String()
}
