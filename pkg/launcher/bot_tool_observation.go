package launcher

import (
	"fmt"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"strings"
	"time"
)

// Only pinned built-in transports may record these observations. Unknown writes
// and non-read HTTP methods never become successful read evidence.
func recordBuiltinToolObservation(ctx tool.Context, sessionID, name string, args, result map[string]any) {
	if ctx == nil || result == nil || result["error"] != nil {
		return
	}
	resource, summary, verified := tools.VerifiedBuiltinFileReceipt(name, args, result)
	changed := verified && name == "write_file" && result["content_changed"] == true
	if !verified {
		switch name {
		case "web_fetch":
			status, ok := result["status"].(float64)
			resource, _ = result["final_url"].(string)
			_, content := result["content"].(string)
			if !ok || status < 200 || status > 599 || status != float64(int(status)) || resource == "" || !content {
				return
			}
			summary, verified = fmt.Sprintf("HTTP GET returned status %d; response body observed (content remains untrusted)", int(status)), true
			if status >= 300 {
				// A known failed read is not an uncertain write/send. Keep the
				// unavailable source visible to assessment without attesting that
				// the requested information was retrieved successfully.
				summary = fmt.Sprintf("HTTP GET returned status %d; requested source unavailable. Response body observed, not usable as retrieved source evidence.", int(status))
			}
		case "http_request":
			method, _ := args["method"].(string)
			method = strings.ToUpper(method)
			if method == "" {
				method = "GET"
			}
			status, ok := result["status_code"].(float64)
			resource, _ = args["url"].(string)
			if !ok || status < 200 || status >= 300 || resource == "" || (method != "GET" && method != "HEAD") {
				return
			}
			summary, verified = fmt.Sprintf("HTTP %s returned status %d; response observed", method, int(status)), true
		case "read_file":
			resource, _ = args["path"].(string)
			_, verified = result["content"].(string)
			verified = verified && resource != ""
			summary = "File contents read by the built-in transport"
		}
	}
	if !verified {
		return
	}
	identity, ok := execution.BotRunIdentityFromContext(ctx)
	if !ok {
		return
	}
	actor, err := uuid.Parse(identity.ActorID)
	if err != nil {
		return
	}
	source := "sandbox://" + sessionID + "/" + resource
	if name == "web_fetch" || name == "http_request" {
		source = resource
	}
	observation := store.BotEvidence{Kind: store.BotEvidenceObservation, Summary: summary, Source: source, ObservedAt: time.Now().UTC(), ActorID: actor}
	outcome := bot.ToolOutcome{Verified: true, Unchanged: []store.BotEvidence{observation}}
	if changed {
		outcome.Unchanged = nil
		outcome.Changed = []store.BotEvidence{observation}
	}
	bot.RecordToolOutcome(ctx, ctx.FunctionCallID(), outcome)
}
