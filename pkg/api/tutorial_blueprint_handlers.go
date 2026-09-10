package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/session"
)

// handleTutorialBlueprintIntent processes Approve / Cancel / Request-changes for
// a pending tutorial blueprint.
//
// Returns handled=true when the HTTP/SSE request is fully finished (cancel,
// errors). On successful Approve it returns handled=false and a rewriteMsg so
// the chat handler can fall through into ChatRunner with the converted drill.
func handleTutorialBlueprintIntent(
	r *http.Request,
	w http.ResponseWriter,
	flusher http.Flusher,
	chatAgent *agent.ChatAgent,
	sessionService session.Service,
	userID, sessionID, msg string,
) (handled bool, rewriteMsg string) {
	trimmed := strings.TrimSpace(msg)

	switch {
	case trimmed == "__tutorial_blueprint_approve__":
		pending := chatAgent.GetPendingTutorialBlueprint(sessionID)
		if pending == nil {
			SendSSE(w, flusher, "error", map[string]interface{}{"error": "No pending tutorial blueprint"})
			SendSSE(w, flusher, "done", map[string]interface{}{"done": true})
			return true, ""
		}
		result, err := tools.BlueprintToTutorialDrillFromYAML(pending.YAML, "")
		if err != nil {
			chatAgent.CancelPendingTutorialBlueprint(sessionID)
			errText := fmt.Sprintf("Failed to convert blueprint: %v", err)
			SendSSE(w, flusher, "text", map[string]interface{}{"text": errText})
			persistSessionMessage(r.Context(), sessionService, userID, sessionID, "model", errText, "")
			SendSSE(w, flusher, "done", map[string]interface{}{"done": true})
			return true, ""
		}
		// Mark approved before clearing pending so validate_drill/save_drill can proceed.
		chatAgent.MarkTutorialBlueprintApproved(sessionID)
		chatAgent.CancelPendingTutorialBlueprint(sessionID)
		payload := map[string]any{
			"title":          pending.Title,
			"suite":          pending.Suite,
			"blueprint_yaml": result.BlueprintYAML,
			"drill_yaml":     result.DrillYAML,
			"drill_name":     result.DrillName,
			"message": result.Message + "\n\nNext: dry-run each screen (clicks + snapshot asserts), " +
				"replace TODOs with multi-step UI, validate_drill, then save_drill. " +
				"Avatar/broll rows remain in the blueprint for a later avatar provider step.",
		}
		SendSSE(w, flusher, "tutorial_blueprint_approved", payload)
		persistTutorialBlueprintApproved(r.Context(), sessionService, userID, sessionID, payload)

		// Fall through into ChatRunner: one user turn carries the drill YAML +
		// continue instructions (no giant model dump, no early done).
		rewriteMsg = fmt.Sprintf(
			"Approve & generate\n\n"+
				"Blueprint approved. Generated tutorial drill %q (%d screen scene(s)).\n\n"+
				"REFINE CHECKLIST (do in order — do NOT run_drill until dry-run is green):\n"+
				"1. Dry-run each screen scene in chat: reach via sidebar/nav clicks (not cold "+
				"browser_navigate except warm-up open_app), perform any reveal interaction "+
				"(e.g. click an options expiration so strikes appear), browser_snapshot + "+
				"browser_take_screenshot (snapshot for asserts; screenshot so the creator sees "+
				"what you see), confirm key content is loaded — not \"Failed to load\" / empty / "+
				"error banners.\n"+
				"2. Replace browser_run_code TODOs with multi-step UI: browser_highlight → "+
				"browser_click(animate_cursor:true) → wait for reveal → optional second click. "+
				"Landing on a URL alone is NOT enough when the visual implies interaction.\n"+
				"3. Forbid inter-scene browser_navigate except warm-up open_app (or a justified deep link).\n"+
				"4. Each recorded screen node MUST have an assert (source: snapshot + contains or "+
				"element_exists) for key content. If the page is broken/empty, fix the stack or "+
				"rewrite the scene — do not save a green drill for a failure state.\n"+
				"5. Keep open_app / enter_fullscreen unrecorded (no narration/record).\n"+
				"6. validate_drill (must pass — rejects TODOs, navigate-only recorded scenes, "+
				"missing asserts) → save_drill into suite %q → then run_drill.\n\n"+
				"```yaml\n%s\n```",
			result.DrillName, result.ScreenCount, pending.Suite, result.DrillYAML,
		)
		return false, rewriteMsg

	case trimmed == "__tutorial_blueprint_cancel__":
		persistSessionMessage(r.Context(), sessionService, userID, sessionID, "user", "Cancel blueprint", "")
		chatAgent.ClearTutorialBlueprintApproved(sessionID)
		chatAgent.CancelPendingTutorialBlueprint(sessionID)
		responseText := "Tutorial blueprint review cancelled."
		SendSSE(w, flusher, "text", map[string]interface{}{"text": responseText})
		persistSessionMessage(r.Context(), sessionService, userID, sessionID, "model", responseText, "")
		SendSSE(w, flusher, "done", map[string]interface{}{"done": true})
		return true, ""

	case trimmed == "__tutorial_blueprint_revise__":
		// Fall through to the agent with a clear revise prompt (keep pending).
		return false, ""

	default:
		// Natural language while a blueprint is pending: treat as revise feedback
		// unless it looks like an unrelated new request — keep pending and let agent run.
		return false, ""
	}
}
