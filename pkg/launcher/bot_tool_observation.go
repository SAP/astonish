package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/adk/tool"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
)

// Only pinned built-in transports may record these observations. Unknown writes
// and non-read HTTP methods never become successful read evidence.
func recordBuiltinToolObservation(ctx tool.Context, sessionID, name string, args, result map[string]any) {
	recordBuiltinToolObservationWithCapture(ctx, sessionID, name, args, result, result)
}

// observation is the trusted transport's decoded metadata; capture is the full
// ADK response journaled for this call. They differ for typed result envelopes.
func recordBuiltinToolObservationWithCapture(ctx tool.Context, sessionID, name string, args, result, capture map[string]any) {
	if ctx == nil || result == nil {
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
	observedAt := time.Now().UTC()
	// ChatAgent's AfterToolCallback journals RedactMap(output), not the raw
	// transport response or Redact(JSON(output)). Bind source facts to that
	// same representation using this run's scoped redactor. Keep the original
	// map for file/effect receipt verification below; never infer facts from
	// provider-supplied metadata or relax the later same-call/digest check.
	sourceResult, sourceCapture := result, capture
	if redactor := credentials.RedactorFromContext(ctx); redactor != nil {
		sourceResult = redactor.RedactMap(result)
		sourceCapture = redactor.RedactMap(capture)
	}
	outcome := bot.ToolOutcome{SourceAccess: builtinSourceAccessFacts(ctx.FunctionCallID(), name, args, sourceResult, sourceCapture, observedAt)}
	defer func() {
		if outcome.Verified || len(outcome.SourceAccess) != 0 {
			bot.RecordToolOutcome(ctx, ctx.FunctionCallID(), outcome)
		}
	}()
	if result["error"] != nil {
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
	source := "sandbox://" + sessionID + "/" + resource
	if name == "web_fetch" || name == "http_request" {
		source = resource
	}
	observation := store.BotEvidence{Kind: store.BotEvidenceObservation, Summary: summary, Source: source, ObservedAt: observedAt, ActorID: actor}
	outcome.Verified = true
	outcome.Unchanged = []store.BotEvidence{observation}
	if changed {
		outcome.Unchanged = nil
		outcome.Changed = []store.BotEvidence{observation}
	}
}

// Access facts are independent of effect receipts: a failed read remains a failed
// read, and a 200 footer says nothing about whether the assignment was answered.
// Hash the whole response JSON, matching the session capture evidence digest.
func builtinSourceAccessFacts(callID, name string, args, result, capture map[string]any, observedAt time.Time) []store.BotSourceAccessFact {
	if callID == "" || (name != "web_fetch" && name != "http_request") {
		return nil
	}
	statusKey := "status"
	if name == "http_request" {
		method, _ := args["method"].(string)
		method = strings.ToUpper(method)
		if method != "" && method != "GET" && method != "HEAD" {
			return nil
		}
		statusKey = "status_code"
	}
	originalURL, _ := args["url"].(string)
	if strings.TrimSpace(originalURL) == "" {
		return nil
	}
	status, validStatus := result[statusKey].(float64)
	validStatus = validStatus && status >= 100 && status <= 599 && status == float64(int(status))
	failed := result["error"] != nil
	if !failed && !validStatus {
		return nil
	}
	// Persistence decodes typed ADK envelopes into maps. Normalize before
	// hashing so struct field order cannot differ from the captured JSON map.
	raw, err := json.Marshal(capture)
	if err != nil {
		return nil
	}
	var persisted map[string]any
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return nil
	}
	raw, err = json.Marshal(persisted)
	if err != nil {
		return nil
	}
	digest := sha256.Sum256(raw)
	fact := store.BotSourceAccessFact{
		EvidenceRef: "capture:" + callID, CallID: callID, OriginalURL: originalURL,
		Status: store.BotSourceAccessFailed, ObservedAt: observedAt,
		BodyDigest: hex.EncodeToString(digest[:]),
	}
	fact.FinalURL, _ = result["final_url"].(string)
	fact.Truncated, _ = result["truncated"].(bool)
	if validStatus {
		fact.HTTPStatus = int(status)
		if !failed && status >= 200 && status < 300 {
			fact.Status = store.BotSourceAccessReturned
		}
	}
	return []store.BotSourceAccessFact{fact}
}
