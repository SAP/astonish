package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/credentials"
	adrill "github.com/SAP/astonish/pkg/drill"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// SessionTitleSetter is a minimal interface for setting a session's title.
// Both *persistentsession.FileStore and store.SessionStore (pgSessionStore)
// satisfy this interface.
type SessionTitleSetter interface {
	SetSessionTitle(ctx context.Context, sessionID, title string) error
}

// SessionTitleChecker extends SessionTitleSetter with the ability to check
// whether a session already has a title. Used by the retry-on-subsequent-message
// logic to avoid redundant LLM calls.
type SessionTitleChecker interface {
	SessionTitleSetter
	GetSessionTitle(ctx context.Context, sessionID string) (string, error)
}

// titleThinkTagRe strips <think>/<thinking> blocks that some models emit in
// title-generation responses.
var titleThinkTagRe = regexp.MustCompile(`(?s)<(?:think|thinking)>.*?</(?:think|thinking)>`)

// userTimestampRe matches the timestamp prefix injected by NewTimestampedUserContent.
// Format: "[2026-03-20 14:30:05 UTC]\n"
var userTimestampRe = regexp.MustCompile(`^\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} \w+\]\n`)

// pendingDelegation holds buffered data from a delegate_tasks FunctionCall
// until the matching FunctionResponse arrives, so we can reconstruct a
// complete subtask_execution message.
type pendingDelegation struct {
	tasks []SubTaskInfoMsg // task plan from the call args
}

// eventsToMessages transforms ADK session events into a flat message list for the frontend.
// An optional redactor is applied to all text parts and tool args/results to prevent
// credential exposure. This is the defense-in-depth layer: even if retroactive transcript
// redaction missed a secret, the UI will never display it in plaintext.
//
// delegate_tasks tool calls and their responses are reconstructed into
// subtask_execution messages that render as TaskPlanPanel in the frontend,
// preserving the task plan visualization for completed sessions.
func eventsToMessages(events session.Events, redactor *credentials.Redactor) []StudioMessage {
	var messages []StudioMessage
	var lastInvocationID string // track invocation boundary for coalescing

	// Buffer delegate_tasks calls by their FunctionCall ID so we can match
	// them with the corresponding FunctionResponse. ADK function calls carry
	// a unique ID that the response echoes back.
	pendingDelegations := make(map[string]*pendingDelegation)

	// Buffer write_file/edit_file calls so we can emit inline artifact
	// messages when the corresponding FunctionResponse confirms success.
	type pendingWriteFile struct {
		path     string
		toolName string
	}
	pendingWriteFiles := make(map[string]pendingWriteFile)

	for i := range events.Len() {
		event := events.At(i)
		if event.LLMResponse.Content == nil {
			continue
		}

		role := string(event.LLMResponse.Content.Role)
		eventInvID := event.InvocationID

		for _, part := range event.LLMResponse.Content.Parts {
			if part.Text != "" {
				msgType := "agent"
				text := part.Text
				if role == "user" {
					msgType = "user"
					// Strip the timestamp prefix injected by NewTimestampedUserContent.
					// Format: "[2026-03-20 14:30:05 UTC]\n<text>"
					text = stripUserMessageTimestamp(text)
				}

				// Check for structured distill messages (model only).
				// These are persisted with a special prefix marker and must
				// be reconstructed as their own message types, not coalesced.
				if role == "model" {
					// Report markers carry no on-screen chat bubble; their
					// effect is to flag matching artifacts as reports. Skip
					// the text entirely so it is neither rendered nor
					// coalesced into the surrounding agent prose.
					if tryParseReportMarkerMessage(text) {
						lastInvocationID = eventInvID
						continue
					}
					if dm := tryParseDistillMessage(text); dm != nil {
						messages = append(messages, *dm)
						lastInvocationID = eventInvID
						continue
					}
					if bm := tryParseTutorialBlueprintMessage(text); bm != nil {
						messages = append(messages, *bm)
						lastInvocationID = eventInvID
						continue
					}
					if sm := tryParseTutorialSceneSlideshowMessage(text); sm != nil {
						messages = append(messages, *sm)
						lastInvocationID = eventInvID
						continue
					}
					if am := tryParseAppPreviewMessage(text); am != nil {
						messages = append(messages, *am)
						lastInvocationID = eventInvID
						continue
					}
					if fm := tryParseFlowOutputMessage(text); fm != nil {
						messages = append(messages, *fm)
						lastInvocationID = eventInvID
						continue
					}
				}

				// Defense-in-depth: redact any credential values from text
				// before sending to the frontend. This catches secrets in user
				// messages and agent responses that may have been persisted
				// before the credential was registered with the redactor.
				if redactor != nil {
					text = redactor.Redact(text)
				}
				// Coalesce with previous message of same type, but only within
				// the same invocation. Different invocations represent separate
				// user turns — merging across them produces garbled messages
				// (e.g. when multiple user messages have no model response between them).
				if len(messages) > 0 && eventInvID == lastInvocationID {
					last := &messages[len(messages)-1]
					if last.Type == msgType && last.ToolName == "" {
						last.Content += text
						continue
					}
				}
				messages = append(messages, StudioMessage{
					Type:    msgType,
					Content: text,
				})
				lastInvocationID = eventInvID
			}
			// Handle InlineData parts.
			// User: file attachments on the most recent user message.
			// Model: standalone image messages (e.g. Gemini image generation).
			if part.InlineData != nil {
				if role == "user" {
					att := AttachmentInfo{
						Filename: part.InlineData.DisplayName,
						MimeType: part.InlineData.MIMEType,
						Size:     len(part.InlineData.Data),
					}
					// Include base64 data for images (enables inline thumbnail display)
					if strings.HasPrefix(part.InlineData.MIMEType, "image/") {
						att.Data = base64.StdEncoding.EncodeToString(part.InlineData.Data)
					}
					// Attach to the most recent user message
					for i := len(messages) - 1; i >= 0; i-- {
						if messages[i].Type == "user" {
							messages[i].Attachments = append(messages[i].Attachments, att)
							break
						}
					}
				} else if strings.HasPrefix(part.InlineData.MIMEType, "image/") && len(part.InlineData.Data) > 0 {
					messages = append(messages, StudioMessage{
						Type:     "image",
						Data:     base64.StdEncoding.EncodeToString(part.InlineData.Data),
						MimeType: part.InlineData.MIMEType,
					})
				}
			}
			if part.FunctionCall != nil {
				// Intercept delegate_tasks calls: buffer the task plan and
				// suppress the flat tool_call message. The matching
				// FunctionResponse below will produce a subtask_execution
				// message instead.
				if part.FunctionCall.Name == "delegate_tasks" {
					pd := extractDelegationPlan(part.FunctionCall.Args)
					pendingDelegations[part.FunctionCall.ID] = pd
					continue
				}

				// Intercept announce_plan: emit a plan message immediately.
				if part.FunctionCall.Name == "announce_plan" {
					msg := buildPlanMessage(part.FunctionCall.Args)
					messages = append(messages, msg)
					continue
				}

				// Intercept update_plan: update the most recent plan message's step.
				if part.FunctionCall.Name == "update_plan" {
					applyPlanStepUpdate(messages, part.FunctionCall.Args)
					continue
				}

				// Buffer write_file/edit_file calls to emit artifact messages
				// when their FunctionResponse confirms success.
				if part.FunctionCall.Name == "write_file" || part.FunctionCall.Name == "edit_file" {
					p := extractFilePath(part.FunctionCall.Name, part.FunctionCall.Args)
					if p != "" {
						pendingWriteFiles[part.FunctionCall.ID] = pendingWriteFile{
							path:     p,
							toolName: part.FunctionCall.Name,
						}
					}
				}

				args := part.FunctionCall.Args
				if redactor != nil && args != nil {
					args = redactor.RedactMap(args)
				}
				messages = append(messages, StudioMessage{
					Type:     "tool_call",
					ToolName: part.FunctionCall.Name,
					ToolID:   part.FunctionCall.ID,
					ToolArgs: args,
				})
			}
			if part.FunctionResponse != nil {
				// Intercept delegate_tasks responses: combine with the
				// buffered task plan to produce a subtask_execution message.
				if part.FunctionResponse.Name == "delegate_tasks" {
					msg := buildSubTaskExecutionMessage(
						pendingDelegations[part.FunctionResponse.ID],
						part.FunctionResponse.Response,
					)
					delete(pendingDelegations, part.FunctionResponse.ID)
					messages = append(messages, msg)
					continue
				}

				// Suppress plan tool responses — no useful display info.
				if part.FunctionResponse.Name == "announce_plan" || part.FunctionResponse.Name == "update_plan" {
					continue
				}

				resp := part.FunctionResponse.Response
				if redactor != nil && resp != nil {
					resp = redactor.RedactMap(resp)
				}
				messages = append(messages, StudioMessage{
					Type:       "tool_result",
					ToolName:   part.FunctionResponse.Name,
					ToolID:     part.FunctionResponse.ID,
					ToolResult: summarizeToolResult(resp),
				})

				// Emit inline artifact message for successful write_file/edit_file
				if pw, ok := pendingWriteFiles[part.FunctionResponse.ID]; ok {
					delete(pendingWriteFiles, part.FunctionResponse.ID)
					// Only emit if the tool succeeded (no error in response)
					hasError := false
					if resp != nil {
						if _, errField := resp["error"]; errField {
							hasError = true
						}
					}
					if !hasError {
						messages = append(messages, StudioMessage{
							Type:     "artifact",
							Content:  pw.path,
							ToolName: pw.toolName,
						})
					}
				}
			}
		}
	}

	// Post-process: for completed sessions, promote any remaining "pending" or
	// "running" plan steps to "complete". The session is done, so all steps
	// should reflect their final state. Only "failed" steps stay as-is.
	finalizePlanSteps(messages)

	return injectTutorialSceneSlideshows(messages, events)
}

// extractDelegationPlan parses the task plan from a delegate_tasks FunctionCall args map.
func extractDelegationPlan(args map[string]any) *pendingDelegation {
	pd := &pendingDelegation{}
	if args == nil {
		return pd
	}

	tasksRaw, ok := args["tasks"]
	if !ok {
		return pd
	}

	tasksList, ok := tasksRaw.([]any)
	if !ok {
		return pd
	}

	for _, t := range tasksList {
		taskMap, ok := t.(map[string]any)
		if !ok {
			continue
		}
		name, _ := taskMap["name"].(string)
		// The description field is called "task" in the tool input schema
		desc, _ := taskMap["task"].(string)
		if name != "" {
			pd.tasks = append(pd.tasks, SubTaskInfoMsg{
				Name:        name,
				Description: desc,
			})
		}
	}
	return pd
}

// buildSubTaskExecutionMessage reconstructs a subtask_execution message from
// a buffered delegation plan and the delegate_tasks response.
func buildSubTaskExecutionMessage(pd *pendingDelegation, resp map[string]any) StudioMessage {
	msg := StudioMessage{
		Type:   "subtask_execution",
		Status: "complete",
	}

	// Use tasks from the buffered call if available
	if pd != nil {
		msg.Tasks = pd.tasks
	}

	// Build synthetic events from the response results
	var events []SubTaskEventMsg

	// Opening event
	events = append(events, SubTaskEventMsg{
		Type: "delegation_start",
	})

	if resp != nil {
		// Extract overall status
		if status, ok := resp["status"].(string); ok {
			msg.Status = status
		}

		// Extract per-task results
		if resultsRaw, ok := resp["results"]; ok {
			if resultsList, ok := resultsRaw.([]any); ok {
				for _, r := range resultsList {
					rMap, ok := r.(map[string]any)
					if !ok {
						continue
					}
					taskName, _ := rMap["name"].(string)
					taskStatus, _ := rMap["status"].(string)
					taskDuration, _ := rMap["duration"].(string)
					taskError, _ := rMap["error"].(string)
					taskResult, _ := rMap["result"].(string)

					// Emit task_start
					events = append(events, SubTaskEventMsg{
						Type:     "task_start",
						TaskName: taskName,
					})

					// Emit task_text with the sub-agent's result output (if any)
					if taskResult != "" {
						events = append(events, SubTaskEventMsg{
							Type:     "task_text",
							TaskName: taskName,
							Text:     taskResult,
						})
					}

					// Emit task_complete or task_failed
					if taskStatus == "success" {
						events = append(events, SubTaskEventMsg{
							Type:     "task_complete",
							TaskName: taskName,
							Duration: taskDuration,
						})
					} else {
						events = append(events, SubTaskEventMsg{
							Type:     "task_failed",
							TaskName: taskName,
							Duration: taskDuration,
							Error:    taskError,
						})
					}
				}
			}
		}
	}

	// Closing event
	events = append(events, SubTaskEventMsg{
		Type:   "delegation_complete",
		Status: msg.Status,
	})

	msg.Events = events

	// If we didn't get tasks from the call args (edge case: missing FunctionCall ID match),
	// try to reconstruct task names from the response results.
	if len(msg.Tasks) == 0 && resp != nil {
		if resultsRaw, ok := resp["results"]; ok {
			if resultsList, ok := resultsRaw.([]any); ok {
				for _, r := range resultsList {
					rMap, ok := r.(map[string]any)
					if !ok {
						continue
					}
					name, _ := rMap["name"].(string)
					if name != "" {
						msg.Tasks = append(msg.Tasks, SubTaskInfoMsg{
							Name: name,
						})
					}
				}
			}
		}
	}

	return msg
}

// buildPlanMessage creates a plan message from an announce_plan FunctionCall args map.
func buildPlanMessage(args map[string]any) StudioMessage {
	msg := StudioMessage{
		Type: "plan",
	}

	if args == nil {
		return msg
	}

	msg.Goal, _ = args["goal"].(string)

	if stepsRaw, ok := args["steps"]; ok {
		if stepsList, ok := stepsRaw.([]any); ok {
			for _, s := range stepsList {
				sMap, ok := s.(map[string]any)
				if !ok {
					continue
				}
				name, _ := sMap["name"].(string)
				desc, _ := sMap["description"].(string)
				if name != "" {
					msg.Steps = append(msg.Steps, PlanStepMsg{
						Name:        name,
						Description: desc,
						Status:      "pending",
					})
				}
			}
		}
	}

	return msg
}

// applyPlanStepUpdate updates the most recent plan message's step status
// based on an update_plan FunctionCall args map.
func applyPlanStepUpdate(messages []StudioMessage, args map[string]any) {
	if args == nil {
		return
	}

	stepName, _ := args["step"].(string)
	stepStatus, _ := args["status"].(string)
	if stepName == "" || stepStatus == "" {
		return
	}

	// Find the most recent plan message (search from end)
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Type == "plan" {
			for j := range messages[i].Steps {
				if messages[i].Steps[j].Name == stepName {
					messages[i].Steps[j].Status = stepStatus
				}
			}
			return
		}
	}
}

// finalizePlanSteps promotes any "pending" or "running" plan steps to "complete"
// for completed session history. Only "failed" steps are left as-is.
func finalizePlanSteps(messages []StudioMessage) {
	for i := range messages {
		if messages[i].Type == "plan" {
			for j := range messages[i].Steps {
				s := messages[i].Steps[j].Status
				if s == "pending" || s == "running" {
					messages[i].Steps[j].Status = "complete"
				}
			}
		}
	}
}

// collectUsage sums UsageMetadata from all LLM responses in the session
// transcript. Each non-partial event with a non-nil UsageMetadata represents
// one API call's token counts. Returns nil if no usage data is present.
func collectUsage(events session.Events) *UsageSummary {
	var input, output, total int32
	for i := range events.Len() {
		event := events.At(i)
		if um := event.LLMResponse.UsageMetadata; um != nil {
			input += um.PromptTokenCount
			output += um.CandidatesTokenCount
			total += um.TotalTokenCount
		}
	}
	if total == 0 {
		return nil
	}
	return &UsageSummary{
		InputTokens:  input,
		OutputTokens: output,
		TotalTokens:  total,
	}
}

// collectArtifacts scans ADK session events for successful write_file/edit_file
// tool calls and returns a deduplicated list of file artifacts. This is used to
// populate the artifacts field in the session detail API response, enabling the
// file panel in the UI for completed sessions.
func collectArtifacts(events session.Events) []ArtifactInfo {
	// Track pending write_file/edit_file calls by FunctionCall ID
	type pendingWrite struct {
		path     string
		toolName string
	}
	pending := make(map[string]pendingWrite)

	// Deduplicate by path while keeping the most recent successful mutation for
	// each file. Reports often get revised through edit_file after their initial
	// write_file; the artifact metadata must point at that latest mutation.
	artifactIndex := make(map[string]int)
	var artifacts []ArtifactInfo

	addArtifact := func(path, toolName string) {
		if path == "" {
			return
		}
		info := ArtifactInfo{
			Path:     path,
			FileName: filepath.Base(path),
			FileType: fileTypeFromExt(filepath.Ext(path)),
			ToolName: toolName,
		}
		if idx, ok := artifactIndex[path]; ok {
			artifacts[idx] = info
			return
		}
		artifactIndex[path] = len(artifacts)
		artifacts = append(artifacts, info)
	}

	for i := range events.Len() {
		event := events.At(i)
		if event.LLMResponse.Content == nil {
			continue
		}

		for _, part := range event.LLMResponse.Content.Parts {
			if part.FunctionCall != nil {
				name := part.FunctionCall.Name
				if name == "write_file" || name == "edit_file" {
					path := extractFilePath(name, part.FunctionCall.Args)
					if path != "" {
						pending[part.FunctionCall.ID] = pendingWrite{
							path:     path,
							toolName: name,
						}
					}
				}
				if name == "browser_stop_recording" || name == "run_drill" {
					// Path(s) are only known after the tool responds; mark pending
					// and fill from FunctionResponse.
					pending[part.FunctionCall.ID] = pendingWrite{
						toolName: name,
					}
				}
			}
			if part.FunctionResponse != nil {
				pw, ok := pending[part.FunctionResponse.ID]
				if !ok {
					continue
				}
				delete(pending, part.FunctionResponse.ID)

				// Check if the tool succeeded (no error in response)
				resp := part.FunctionResponse.Response
				if resp != nil {
					if _, hasErr := resp["error"]; hasErr {
						continue
					}
				}

				switch pw.toolName {
				case "browser_stop_recording":
					if resp != nil {
						if path, ok := resp["path"].(string); ok && path != "" {
							pw.path = path
						}
					}
					addArtifact(pw.path, pw.toolName)
				case "run_drill":
					if resp != nil {
						for _, p := range extractArtifactPathsFromRunDrillResponse(resp) {
							addArtifact(p, "run_drill")
						}
					}
				default:
					addArtifact(pw.path, pw.toolName)
				}
			}
		}
	}

	return artifacts
}

// extractArtifactPathsFromRunDrillResponse reads artifact_paths / manifest_path
// from a persisted run_drill FunctionResponse (values may be []any or []string).
func extractArtifactPathsFromRunDrillResponse(resp map[string]any) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	switch v := resp["artifact_paths"].(type) {
	case []string:
		for _, p := range v {
			add(p)
		}
	case []any:
		for _, item := range v {
			if p, ok := item.(string); ok {
				add(p)
			}
		}
	}
	if p, ok := resp["manifest_path"].(string); ok {
		add(p)
	}
	return out
}

// extractFilePath gets the file path from a write_file or edit_file tool call args.
func extractFilePath(toolName string, args map[string]any) string {
	if args == nil {
		return ""
	}
	var p string
	switch toolName {
	case "write_file":
		p, _ = args["file_path"].(string)
	case "edit_file":
		p, _ = args["path"].(string)
	}
	if p == "" {
		return ""
	}
	// Ensure path is absolute for consistent artifact resolution
	if !filepath.IsAbs(p) {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	return p
}

// fileTypeFromExt returns a human-readable file type from a file extension.
func fileTypeFromExt(ext string) string {
	ext = strings.ToLower(ext)
	switch ext {
	case ".md", ".markdown":
		return "Markdown"
	case ".go":
		return "Go"
	case ".py":
		return "Python"
	case ".js":
		return "JavaScript"
	case ".ts":
		return "TypeScript"
	case ".tsx":
		return "TypeScript JSX"
	case ".jsx":
		return "JSX"
	case ".json":
		return "JSON"
	case ".yaml", ".yml":
		return "YAML"
	case ".html", ".htm":
		return "HTML"
	case ".css":
		return "CSS"
	case ".sh", ".bash":
		return "Shell"
	case ".sql":
		return "SQL"
	case ".txt":
		return "Text"
	case ".csv":
		return "CSV"
	case ".xml":
		return "XML"
	case ".toml":
		return "TOML"
	case ".rs":
		return "Rust"
	case ".java":
		return "Java"
	case ".rb":
		return "Ruby"
	case ".php":
		return "PHP"
	case ".c", ".h":
		return "C"
	case ".cpp", ".hpp", ".cc":
		return "C++"
	case ".swift":
		return "Swift"
	case ".kt":
		return "Kotlin"
	case ".dockerfile":
		return "Dockerfile"
	case ".env":
		return "Environment"
	case ".mp4", ".webm", ".mov":
		return "Video"
	default:
		if ext == "" {
			return "File"
		}
		return strings.TrimPrefix(ext, ".")
	}
}

// stripUserMessageTimestamp removes the timestamp prefix from a user message
// that was injected by NewTimestampedUserContent. This keeps the timestamp in
// the persisted event (for the LLM's context) while displaying clean text to
// the user in the Studio UI.
func stripUserMessageTimestamp(text string) string {
	return userTimestampRe.ReplaceAllString(text, "")
}

// summarizeToolResult converts a tool response map into a display-friendly value.
func summarizeToolResult(resp map[string]any) interface{} {
	if resp == nil {
		return nil
	}
	if v, ok := resp["result"]; ok {
		if s, ok := v.(string); ok {
			return truncateResult(s)
		}
	}
	if v, ok := resp["output"]; ok {
		if s, ok := v.(string); ok {
			return truncateResult(s)
		}
	}
	if v, ok := resp["error"]; ok {
		return v
	}
	return resp
}

// truncateResult limits tool output to a reasonable length for display.
func truncateResult(s string) string {
	const maxLen = 2000
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n\n... (truncated)"
}

// fleetEventsToMessages converts ADK events from a fleet transcript back into
// fleet-style message summaries for the frontend.
func fleetEventsToMessages(events []*session.Event) []FleetMessageSummary {
	var messages []FleetMessageSummary
	for _, event := range events {
		if event.LLMResponse.Content == nil {
			continue
		}
		// Extract text from all parts
		var text string
		for _, part := range event.LLMResponse.Content.Parts {
			if part.Text != "" {
				text += part.Text
			}
		}
		if text == "" {
			continue
		}

		// Determine sender from Author field (set by fleetMessageToEvent)
		sender := event.Author
		if sender == "" {
			// Fallback: infer from role
			if event.LLMResponse.Content.Role == genai.RoleUser {
				sender = "customer"
			} else {
				sender = "agent"
			}
		}
		if sender == "user" {
			sender = "customer"
		}

		messages = append(messages, FleetMessageSummary{
			ID:        event.ID,
			Sender:    sender,
			Text:      text,
			Timestamp: event.Timestamp.Format(time.RFC3339Nano),
		})
	}
	return messages
}

// titleRefineTimeout bounds the best-effort LLM title polish. Kept under the
// production SSE titleWaitTimeout (30s) so a successful refine can still flush
// before subscribers close. The provisional fallback title is already on screen.
const titleRefineTimeout = 25 * time.Second

// sessionNeedsTitle reports whether this turn should assign/refresh a title.
func sessionNeedsTitle(ctx context.Context, sessionID string, isNew bool, msg string, titleSetter SessionTitleSetter) bool {
	if msg == "" || titleSetter == nil {
		return false
	}
	if isNew {
		return true
	}
	checker, ok := titleSetter.(SessionTitleChecker)
	if !ok {
		return false
	}
	existing, err := checker.GetSessionTitle(ctx, sessionID)
	return err == nil && existing == ""
}

// setSessionTitleNow persists a title and invokes onTitle on success.
func setSessionTitleNow(store SessionTitleSetter, sessionID, title string, onTitle func(string)) {
	if store == nil || title == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.SetSessionTitle(ctx, sessionID, title); err != nil {
		slog.Warn("failed to set session title", "session_id", sessionID, "error", err)
		return
	}
	if onTitle != nil {
		onTitle(title)
	}
}

// cleanLLMTitle strips thinking tags/preambles and truncates to 80 chars.
// Returns empty string when the model output is unusable.
func cleanLLMTitle(raw string) string {
	title := titleThinkTagRe.ReplaceAllString(raw, "")
	// Also strip unclosed thinking tags (model hit token limit mid-tag)
	if idx := strings.Index(title, "<think"); idx >= 0 {
		title = title[:idx]
	}
	if idx := strings.Index(title, "<thinking"); idx >= 0 {
		title = title[:idx]
	}
	title = stripThinkingPreamble(title)
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	if len(title) > 80 {
		title = title[:77] + "..."
	}
	return title
}

// generateStudioSessionTitle best-effort LLM polish of a session title.
// provisionalTitle is the title already shown/persisted from fallbackTitle; when
// set, LLM errors or empty output are a no-op (provisional stays). When empty,
// falls back to fallbackTitle on failure (defense in depth).
// onTitle is invoked only when a new title is successfully persisted.
func generateStudioSessionTitle(llm model.LLM, store SessionTitleSetter, sessionID, userMessage, provisionalTitle string, onTitle func(string)) {
	ctx, cancel := context.WithTimeout(context.Background(), titleRefineTimeout)
	defer cancel()

	prompt := fmt.Sprintf(
		"Generate a concise title (5-7 words max) for a conversation that starts with this message. "+
			"Return ONLY the title text, nothing else. "+
			"Do not include any thinking, reasoning, analysis, or explanation. "+
			"No quotes, no markdown, no punctuation at the end.\n\nUser message: %s", userMessage)

	req := &model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromText(prompt, genai.RoleUser),
		},
		Config: &genai.GenerateContentConfig{
			Temperature:     genai.Ptr(float32(0.3)),
			MaxOutputTokens: 100,
		},
	}

	var raw string
	for resp, err := range llm.GenerateContent(ctx, req, false) {
		if err != nil {
			slog.Warn("session title LLM error", "session_id", sessionID, "error", err)
			if provisionalTitle == "" {
				setSessionTitleNow(store, sessionID, fallbackTitle(userMessage), onTitle)
			}
			return
		}
		if resp.Content == nil {
			continue
		}
		for _, part := range resp.Content.Parts {
			if part.Text != "" && !part.Thought {
				raw += part.Text
			}
		}
	}

	title := cleanLLMTitle(raw)
	if title == "" {
		if provisionalTitle != "" {
			return
		}
		title = fallbackTitle(userMessage)
		if title == "" {
			slog.Debug("session title generation produced empty result", "session_id", sessionID)
			return
		}
	}
	if title == provisionalTitle {
		return
	}
	setSessionTitleNow(store, sessionID, title, onTitle)
}

// thinkingPreambleRe matches common plain-text thinking preambles that models
// like Qwen emit at the start of their response instead of using XML tags.
var thinkingPreambleRe = regexp.MustCompile(`(?i)^(thinking process|thinking|reasoning|let me think|analysis|thought process)\s*:?\s*\n`)

// stripThinkingPreamble removes plain-text thinking/reasoning content from
// a title string. If the text starts with a thinking header followed by
// multi-line reasoning, we extract only the last short line (the actual title).
// If no clean title line is found, returns empty string (caller uses fallback).
func stripThinkingPreamble(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	// Check if the text looks like thinking output:
	// - starts with a known thinking preamble, OR
	// - contains numbered lists / markdown bold patterns typical of reasoning
	looksLikeThinking := thinkingPreambleRe.MatchString(text) ||
		(strings.Contains(text, "\n") && (strings.Contains(text, "1.") || strings.Contains(text, "**")))

	if !looksLikeThinking {
		return text
	}

	// The actual title is typically the last short non-empty line after
	// the model finishes its thinking. Walk lines backwards.
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		// Skip empty lines, markdown formatting, numbered items, and long lines
		if line == "" {
			continue
		}
		if len(line) > 80 {
			continue
		}
		// Skip lines that look like reasoning (numbered, bold markers, bullets)
		if len(line) > 0 && (line[0] == '-' || line[0] == '*' || line[0] == '#') {
			continue
		}
		if len(line) > 1 && line[0] >= '0' && line[0] <= '9' && (line[1] == '.' || line[1] == ')') {
			continue
		}
		if len(line) > 2 && line[0] >= '0' && line[0] <= '9' && line[1] >= '0' && line[1] <= '9' && (line[2] == '.' || line[2] == ')') {
			continue
		}
		// Skip lines with thinking preamble pattern
		if thinkingPreambleRe.MatchString(line + "\n") {
			continue
		}
		// This looks like a clean title line — strip any surrounding quotes/markdown
		line = strings.Trim(line, "\"'`*_")
		if line != "" {
			return line
		}
	}

	return ""
}

// fallbackTitle derives a short title from the user message when LLM title
// generation fails or produces empty output.
func fallbackTitle(userMessage string) string {
	// Strip timestamp prefix if present (e.g., "[2026-03-20 14:30:05 UTC]\n")
	msg := userTimestampRe.ReplaceAllString(userMessage, "")
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}
	// Take up to 50 characters, breaking at word boundary
	if len(msg) <= 50 {
		return msg
	}
	// Find last space before 50 chars
	cut := strings.LastIndex(msg[:50], " ")
	if cut <= 10 {
		cut = 50
	}
	return msg[:cut] + "..."
}

// toInt converts an interface{} to int (handles float64 from JSON).
func toInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case int64:
		return int(n)
	default:
		return 0
	}
}

// persistRunError saves a synthetic model event to the session when the runner
// returns an error. Without this, errors are only sent as transient SSE events
// and disappear on page reload — the user sees their message but no indication
// that the model failed.
func persistRunError(ctx context.Context, svc session.Service, userID, sessionID string, runErr error) {
	resp, err := svc.Get(ctx, &session.GetRequest{
		AppName:   studioChatAppName,
		UserID:    userID,
		SessionID: sessionID,
	})
	if err != nil {
		slog.Error("failed to get session", "component", "persistRunError", "session_id", sessionID, "error", err)
		return
	}

	errorEvent := &session.Event{
		ID:        fmt.Sprintf("error-%d", time.Now().UnixMilli()),
		Author:    "model",
		Timestamp: time.Now(),
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: fmt.Sprintf("[Error: %s]", runErr.Error())}},
			},
		},
	}

	if err := svc.AppendEvent(ctx, resp.Session, errorEvent); err != nil {
		slog.Error("failed to append error event to session", "component", "persistRunError", "session_id", sessionID, "error", err)
	}
}

// persistSessionMessage appends a user or model text message to the session.
// This is used for interactions that bypass runner.Run() (like slash commands)
// so they appear in the persisted session history.
func persistSessionMessage(ctx context.Context, svc session.Service, userID, sessionID, role, text string) {
	if svc == nil || sessionID == "" || text == "" {
		return
	}
	resp, err := svc.Get(ctx, &session.GetRequest{
		AppName:   studioChatAppName,
		UserID:    userID,
		SessionID: sessionID,
	})
	if err != nil {
		slog.Error("failed to get session for persist", "component", "persistSessionMessage", "session_id", sessionID, "error", err)
		return
	}

	author := role
	if role == "model" {
		author = "model"
	}

	event := &session.Event{
		ID:        fmt.Sprintf("%s-%d", role, time.Now().UnixMilli()),
		Author:    author,
		Timestamp: time.Now(),
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{
				Role:  role,
				Parts: []*genai.Part{{Text: text}},
			},
		},
	}

	if err := svc.AppendEvent(ctx, resp.Session, event); err != nil {
		slog.Error("failed to append event to session", "component", "persistSessionMessage", "session_id", sessionID, "role", role, "error", err)
	}
}

// readArtifactContentFromSession scans a session's persisted events for the
// latest write_file/edit_file mutation whose path matches the requested path,
// and reconstructs the best-known content. This is used only as a fallback when
// the actual file cannot be read from the host or live sandbox.
func readArtifactContentFromSession(fs *persistentsession.FileStore, userID, sessionID, filePath string) (string, bool) {
	if fs == nil {
		return "", false
	}

	getResp, err := fs.Get(context.Background(), &session.GetRequest{
		AppName:   studioChatAppName,
		UserID:    userID,
		SessionID: sessionID,
	})
	if err != nil {
		return "", false
	}

	return readArtifactContentFromEvents(getResp.Session.Events(), filePath)
}

// readArtifactContentFromSessionStore is the platform-mode equivalent of
// readArtifactContentFromSession. It accepts a store.SessionStore (which may
// be backed by PostgreSQL) and uses ReadTranscriptEvents to load events, then
// reconstructs the latest known content for the requested path.
func readArtifactContentFromSessionStore(ss store.SessionStore, appName, userID, sessionID, filePath string) (string, bool) {
	if ss == nil {
		return "", false
	}

	events, err := ss.ReadTranscriptEvents(context.TODO(), appName, userID, sessionID)
	if err != nil {
		return "", false
	}

	return readArtifactContentFromEventSlice(events, filePath)
}

func readArtifactContentFromEvents(events session.Events, filePath string) (string, bool) {
	if events == nil {
		return "", false
	}
	items := make([]*session.Event, 0, events.Len())
	for i := range events.Len() {
		items = append(items, events.At(i))
	}
	return readArtifactContentFromEventSlice(items, filePath)
}

func readArtifactContentFromEventSlice(events []*session.Event, filePath string) (string, bool) {
	cleanTarget := filepath.Clean(filePath)
	var content string
	var known bool
	type pendingMutation struct {
		previousContent string
		previousKnown   bool
	}
	pending := make(map[string]pendingMutation)

	for _, event := range events {
		if event == nil || event.LLMResponse.Content == nil {
			continue
		}
		for _, part := range event.LLMResponse.Content.Parts {
			if part.FunctionCall != nil && part.FunctionCall.Args != nil {
				args := part.FunctionCall.Args
				switch part.FunctionCall.Name {
				case "write_file":
					p, _ := args["file_path"].(string)
					if filepath.Clean(p) != cleanTarget {
						continue
					}
					previous := pendingMutation{previousContent: content, previousKnown: known}
					newContent, _ := args["content"].(string)
					content = normalizeWriteFileFallbackContent(newContent)
					known = true
					if part.FunctionCall.ID != "" {
						pending[part.FunctionCall.ID] = previous
					}
				case "edit_file":
					p, _ := args["path"].(string)
					if !known || filepath.Clean(p) != cleanTarget {
						continue
					}
					updated, ok := applyEditFileFallback(content, args)
					if ok {
						previous := pendingMutation{previousContent: content, previousKnown: known}
						content = updated
						if part.FunctionCall.ID != "" {
							pending[part.FunctionCall.ID] = previous
						}
					}
				}
			}
			if part.FunctionResponse != nil {
				previous, ok := pending[part.FunctionResponse.ID]
				if !ok {
					continue
				}
				delete(pending, part.FunctionResponse.ID)
				if part.FunctionResponse.Response != nil {
					if _, hasErr := part.FunctionResponse.Response["error"]; hasErr {
						content = previous.previousContent
						known = previous.previousKnown
					}
				}
			}
		}
	}
	return content, known
}

func normalizeWriteFileFallbackContent(content string) string {
	if extracted, ok := tryExtractStdoutFallback(content); ok {
		return extracted
	}
	return content
}

func tryExtractStdoutFallback(input string) (string, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", false
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return "", false
	}
	stdout, ok := parsed["stdout"]
	if !ok {
		return "", false
	}
	switch v := stdout.(type) {
	case string:
		return v, true
	case []any:
		lines := make([]string, 0, len(v))
		for _, item := range v {
			lines = append(lines, fmt.Sprintf("%v", item))
		}
		return strings.Join(lines, "\n"), true
	default:
		return "", false
	}
}

func applyEditFileFallback(content string, args map[string]any) (string, bool) {
	oldString, _ := args["old_string"].(string)
	if oldString == "" {
		return content, false
	}
	newString, _ := args["new_string"].(string)
	regexMode, _ := args["regex"].(bool)
	replaceAll, _ := args["replace_all"].(bool)

	if regexMode {
		re, err := regexp.Compile(oldString)
		if err != nil {
			return content, false
		}
		matches := re.FindAllStringIndex(content, -1)
		if len(matches) == 0 || (len(matches) > 1 && !replaceAll) {
			return content, false
		}
		if replaceAll {
			return re.ReplaceAllString(content, newString), true
		}
		match := matches[0]
		matched := content[match[0]:match[1]]
		replaced := re.ReplaceAllString(matched, newString)
		return content[:match[0]] + replaced + content[match[1]:], true
	}

	count := strings.Count(content, oldString)
	if count == 0 || (count > 1 && !replaceAll) {
		return content, false
	}
	if replaceAll {
		return strings.ReplaceAll(content, oldString, newString), true
	}
	return strings.Replace(content, oldString, newString, 1), true
}

// --- Distill preview/saved persistence ---
//
// Distill preview and saved messages are persisted as model text events with a
// special prefix marker so eventsToMessages can reconstruct them as structured
// StudioMessage objects. The format is:
//   [distill_preview]<JSON payload>
//   [distill_saved]<JSON payload>

const distillPreviewPrefix = "[distill_preview]"
const distillSavedPrefix = "[distill_saved]"

// distillPreviewPayload is the JSON structure persisted inside the text event.
type distillPreviewPayload struct {
	YAML        string   `json:"yaml"`
	FlowName    string   `json:"flowName"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
	Explanation string   `json:"explanation,omitempty"`
}

// distillSavedPayload is the JSON structure persisted inside the text event.
type distillSavedPayload struct {
	FilePath   string `json:"filePath"`
	RunCommand string `json:"runCommand"`
}

// persistDistillPreview serializes a DistillReview as a structured text event.
func persistDistillPreview(ctx context.Context, svc session.Service, userID, sessionID string, review *agent.DistillReview) {
	if svc == nil || sessionID == "" || review == nil {
		return
	}
	payload := distillPreviewPayload{
		YAML:        review.YAML,
		FlowName:    review.FlowName,
		Description: review.Description,
		Tags:        review.Tags,
		Explanation: review.Explanation,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Error("failed to marshal distill preview", "error", err)
		return
	}
	persistSessionMessage(ctx, svc, userID, sessionID, "model", distillPreviewPrefix+string(data))
}

// persistDistillSaved serializes a distill-saved result as a structured text event.
func persistDistillSaved(ctx context.Context, svc session.Service, userID, sessionID, filePath, runCmd string) {
	if svc == nil || sessionID == "" {
		return
	}
	payload := distillSavedPayload{
		FilePath:   filePath,
		RunCommand: runCmd,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Error("failed to marshal distill saved", "error", err)
		return
	}
	persistSessionMessage(ctx, svc, userID, sessionID, "model", distillSavedPrefix+string(data))
}

// tryParseDistillMessage checks if a text starts with a distill marker prefix
// and returns a structured StudioMessage if so. Returns nil if not a distill message.
func tryParseDistillMessage(text string) *StudioMessage {
	if strings.HasPrefix(text, distillPreviewPrefix) {
		jsonStr := text[len(distillPreviewPrefix):]
		var payload distillPreviewPayload
		if err := json.Unmarshal([]byte(jsonStr), &payload); err != nil {
			return nil
		}
		return &StudioMessage{
			Type:        "distill_preview",
			YAML:        payload.YAML,
			FlowName:    payload.FlowName,
			Description: payload.Description,
			Tags:        payload.Tags,
			Explanation: payload.Explanation,
		}
	}
	if strings.HasPrefix(text, distillSavedPrefix) {
		jsonStr := text[len(distillSavedPrefix):]
		var payload distillSavedPayload
		if err := json.Unmarshal([]byte(jsonStr), &payload); err != nil {
			return nil
		}
		return &StudioMessage{
			Type:       "distill_saved",
			FilePath:   payload.FilePath,
			RunCommand: payload.RunCommand,
		}
	}
	return nil
}

// --- Tutorial blueprint preview persistence ---

const tutorialBlueprintPreviewPrefix = "[tutorial_blueprint_preview]"
const tutorialBlueprintApprovedPrefix = "[tutorial_blueprint_approved]"

func persistTutorialBlueprintPreview(ctx context.Context, svc session.Service, userID, sessionID string, payload map[string]any) {
	if svc == nil || sessionID == "" || payload == nil {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Error("failed to marshal tutorial blueprint preview", "error", err)
		return
	}
	persistSessionMessage(ctx, svc, userID, sessionID, "model", tutorialBlueprintPreviewPrefix+string(data))
}

func persistTutorialBlueprintApproved(ctx context.Context, svc session.Service, userID, sessionID string, payload map[string]any) {
	if svc == nil || sessionID == "" || payload == nil {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Error("failed to marshal tutorial blueprint approved", "error", err)
		return
	}
	persistSessionMessage(ctx, svc, userID, sessionID, "model", tutorialBlueprintApprovedPrefix+string(data))
}

func tryParseTutorialBlueprintMessage(text string) *StudioMessage {
	if strings.HasPrefix(text, tutorialBlueprintPreviewPrefix) {
		jsonStr := text[len(tutorialBlueprintPreviewPrefix):]
		var payload map[string]any
		if err := json.Unmarshal([]byte(jsonStr), &payload); err != nil {
			return nil
		}
		return studioMessageFromBlueprintPayload("tutorial_blueprint_preview", payload)
	}
	if strings.HasPrefix(text, tutorialBlueprintApprovedPrefix) {
		jsonStr := text[len(tutorialBlueprintApprovedPrefix):]
		var payload map[string]any
		if err := json.Unmarshal([]byte(jsonStr), &payload); err != nil {
			return nil
		}
		return studioMessageFromBlueprintPayload("tutorial_blueprint_approved", payload)
	}
	return nil
}

func studioMessageFromBlueprintPayload(msgType string, payload map[string]any) *StudioMessage {
	msg := &StudioMessage{Type: msgType}
	if v, ok := payload["title"].(string); ok {
		msg.BlueprintTitle = v
	}
	if v, ok := payload["suite"].(string); ok {
		msg.BlueprintSuite = v
	}
	if v, ok := payload["blueprint_yaml"].(string); ok {
		msg.BlueprintYAML = v
	}
	if v, ok := payload["drill_yaml"].(string); ok {
		msg.DrillYAML = v
	}
	if v, ok := payload["drill_name"].(string); ok {
		msg.DrillName = v
	}
	if v, ok := payload["message"].(string); ok {
		msg.Content = v
	}
	if scenes, ok := payload["scenes"].([]any); ok {
		msg.BlueprintScenes = scenes
	}
	return msg
}

// --- Tutorial scene slideshow persistence ---

const tutorialSceneSlideshowPrefix = "[tutorial_scene_slideshow]"

func persistTutorialSceneSlideshow(ctx context.Context, svc session.Service, userID, sessionID string, payload map[string]any) {
	if svc == nil || sessionID == "" || payload == nil {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Error("failed to marshal tutorial scene slideshow", "error", err)
		return
	}
	persistSessionMessage(ctx, svc, userID, sessionID, "model", tutorialSceneSlideshowPrefix+string(data))
}

func tryParseTutorialSceneSlideshowMessage(text string) *StudioMessage {
	if !strings.HasPrefix(text, tutorialSceneSlideshowPrefix) {
		return nil
	}
	jsonStr := text[len(tutorialSceneSlideshowPrefix):]
	var payload map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &payload); err != nil {
		return nil
	}
	return studioMessageFromSlideshowPayload(payload)
}

func studioMessageFromSlideshowPayload(payload map[string]any) *StudioMessage {
	if payload == nil {
		return nil
	}
	msg := &StudioMessage{Type: "tutorial_scene_slideshow"}
	if v, ok := payload["title"].(string); ok {
		msg.TutorialTitle = v
	}
	if v, ok := payload["suite"].(string); ok {
		msg.TutorialSuite = v
	}
	if v, ok := payload["drill"].(string); ok {
		msg.TutorialDrill = v
	}
	if v, ok := payload["manifest_path"].(string); ok {
		msg.ManifestPath = v
	}
	if scenes, ok := payload["scenes"].([]any); ok {
		msg.TutorialScenes = scenes
	}
	return msg
}

func buildTutorialSceneSlideshowPayload(manifestPath string) (map[string]any, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var manifest adrill.SceneManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	title := manifest.Drill
	if title == "" {
		title = filepath.Base(strings.TrimSuffix(manifestPath, filepath.Ext(manifestPath)))
	}
	scenes := make([]map[string]any, 0, len(manifest.Scenes))
	for _, sc := range manifest.Scenes {
		voice := sc.Voiceover
		if voice == "" {
			voice = sc.Narration
		}
		row := map[string]any{
			"id":                 sc.ID,
			"voiceover":          voice,
			"narration":          sc.Narration,
			"visual_kind":        sc.VisualKind,
			"visual_description": sc.VisualDescription,
			"hold_ms":            sc.HoldMs,
		}
		if sc.Path != "" {
			row["path"] = sc.Path
		}
		if sc.DurationSeconds > 0 {
			row["duration_seconds"] = sc.DurationSeconds
		}
		scenes = append(scenes, row)
	}
	return map[string]any{
		"title":         title,
		"suite":         manifest.Suite,
		"drill":         manifest.Drill,
		"manifest_path": manifestPath,
		"scenes":        scenes,
	}, nil
}

func studioMessageFromManifestPath(manifestPath string) *StudioMessage {
	payload, err := buildTutorialSceneSlideshowPayload(manifestPath)
	if err != nil {
		slog.Warn("failed to build tutorial scene slideshow from manifest", "path", manifestPath, "error", err)
		return nil
	}
	return studioMessageFromSlideshowPayload(payload)
}

type slideshowInsert struct {
	afterIdx int
	msg      StudioMessage
}

// injectTutorialSceneSlideshows synthesizes slideshow messages for tutorial
// run_drill results that predate persisted markers (page-refresh recovery).
func injectTutorialSceneSlideshows(messages []StudioMessage, events session.Events) []StudioMessage {
	covered := map[string]bool{}
	for _, m := range messages {
		if m.Type == "tutorial_scene_slideshow" && m.ManifestPath != "" {
			covered[m.ManifestPath] = true
		}
	}

	var pending []slideshowInsert
	runDrillIdx := 0
	for i := range events.Len() {
		ev := events.At(i)
		if ev == nil || ev.Content == nil {
			continue
		}
		for _, part := range ev.Content.Parts {
			if part.FunctionResponse == nil || part.FunctionResponse.Name != "run_drill" {
				continue
			}
			resp := part.FunctionResponse.Response
			if resp == nil {
				runDrillIdx++
				continue
			}
			if status, _ := resp["status"].(string); status == "error" {
				runDrillIdx++
				continue
			}
			manifestPath, _ := resp["manifest_path"].(string)
			if manifestPath == "" {
				runDrillIdx++
				continue
			}
			if covered[manifestPath] {
				runDrillIdx++
				continue
			}
			msg := studioMessageFromManifestPath(manifestPath)
			if msg == nil {
				runDrillIdx++
				continue
			}
			afterIdx := findRunDrillToolResultIndex(messages, runDrillIdx)
			if afterIdx >= 0 {
				pending = append(pending, slideshowInsert{afterIdx: afterIdx, msg: *msg})
				covered[manifestPath] = true
			}
			runDrillIdx++
		}
	}
	if len(pending) == 0 {
		return messages
	}
	// Insert from highest index downward so indices stay valid.
	for i := len(pending) - 1; i >= 0; i-- {
		ins := pending[i]
		idx := ins.afterIdx + 1
		messages = append(messages[:idx], append([]StudioMessage{ins.msg}, messages[idx:]...)...)
	}
	return messages
}

func findRunDrillToolResultIndex(messages []StudioMessage, n int) int {
	count := 0
	for i, m := range messages {
		if m.Type == "tool_result" && m.ToolName == "run_drill" {
			if count == n {
				return i
			}
			count++
		}
	}
	return -1
}

// --- App preview persistence ---
//
// App preview messages are persisted using the same prefix-marker pattern as distill.
// Format: [app_preview]<JSON payload>

const appPreviewPrefix = "[app_preview]"

// appPreviewPayload is the JSON structure persisted inside the text event.
type appPreviewPayload struct {
	Code        string `json:"code"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Version     int    `json:"version"`
	AppID       string `json:"appId,omitempty"`
}

// persistAppPreview serializes an app preview as a structured text event.
func persistAppPreview(ctx context.Context, svc session.Service, userID, sessionID, code, title string, version int, appID string) {
	if svc == nil || sessionID == "" || code == "" {
		return
	}
	payload := appPreviewPayload{
		Code:    code,
		Title:   title,
		Version: version,
		AppID:   appID,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Error("failed to marshal app preview", "error", err)
		return
	}
	persistSessionMessage(ctx, svc, userID, sessionID, "model", appPreviewPrefix+string(data))
}

// tryParseAppPreviewMessage checks if a text starts with the app_preview marker prefix
// and returns a structured StudioMessage if so. Returns nil if not an app preview message.
func tryParseAppPreviewMessage(text string) *StudioMessage {
	if !strings.HasPrefix(text, appPreviewPrefix) {
		return nil
	}
	jsonStr := text[len(appPreviewPrefix):]
	var payload appPreviewPayload
	if err := json.Unmarshal([]byte(jsonStr), &payload); err != nil {
		return nil
	}
	return &StudioMessage{
		Type:        "app_preview",
		AppCode:     payload.Code,
		AppTitle:    payload.Title,
		Description: payload.Description,
		AppVersion:  payload.Version,
		AppID:       payload.AppID,
	}
}

// reconstructActiveApp scans session events for app_preview prefix markers and
// rebuilds the ActiveApp state. Used when the server restarts and in-memory state
// is lost. Returns nil if no app previews are found in the session.
func reconstructActiveApp(events session.Events) *agent.ActiveApp {
	var latestApp *agent.ActiveApp

	for i := range events.Len() {
		event := events.At(i)
		if event.LLMResponse.Content == nil {
			continue
		}
		for _, part := range event.LLMResponse.Content.Parts {
			if part.Text == "" {
				continue
			}
			if !strings.HasPrefix(part.Text, appPreviewPrefix) {
				continue
			}
			jsonStr := part.Text[len(appPreviewPrefix):]
			var payload appPreviewPayload
			if err := json.Unmarshal([]byte(jsonStr), &payload); err != nil {
				continue
			}

			if latestApp == nil || (payload.AppID != "" && payload.AppID != latestApp.AppID) {
				// New app (or first app found)
				latestApp = &agent.ActiveApp{
					AppID:    payload.AppID,
					Title:    payload.Title,
					Code:     payload.Code,
					Versions: []string{},
					Version:  payload.Version,
				}
			} else {
				// Same app, newer version — append previous code to history
				latestApp.Versions = append(latestApp.Versions, latestApp.Code)
				latestApp.Code = payload.Code
				latestApp.Version = payload.Version
				latestApp.Title = payload.Title
			}
		}
	}
	return latestApp
}

// --- Report marker persistence ---
//
// A report marker is the persisted record that an artifact created via
// write_file/edit_file in a given turn was signaled by the agent (via an
// ```astonish-report fence) to be a report. The marker carries the absolute
// path to the artifact and an optional title. It is the source of truth that
// flips ArtifactInfo.IsReport on subsequent session-detail loads.
//
// Why a structured persisted marker (not a flag on the artifact event):
//   - artifact events come from tool execution and predate the agent's text
//     output that contains the fence; we cannot retroactively edit them;
//   - the same marker pattern is already used for app_preview and distill,
//     so this fits the established prefix-marker convention;
//   - decoupling the marker from the artifact preserves the one-event-one-
//     concern rule and lets future report metadata (export formats, summary)
//     attach without touching the artifact schema.

const reportMarkerPrefix = "[report_marker]"

// reportMarkerPayload is the JSON record persisted inside the text event
// that follows a report-bearing tool call. Path is required; title is
// optional and may be empty.
type reportMarkerPayload struct {
	Path  string `json:"path"`
	Title string `json:"title,omitempty"`
}

// persistReportMarker serializes a report marker as a structured text event
// appended to the session log. Mirrors persistAppPreview in shape and
// failure handling — log-and-skip on errors so a persistence hiccup never
// breaks the live SSE stream.
func persistReportMarker(ctx context.Context, svc session.Service, userID, sessionID, path, title string) {
	if svc == nil || sessionID == "" || path == "" {
		return
	}
	payload := reportMarkerPayload{Path: path, Title: title}
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Error("failed to marshal report marker", "component", "persistReportMarker", "error", err)
		return
	}
	persistSessionMessage(ctx, svc, userID, sessionID, "model", reportMarkerPrefix+string(data))
}

// collectReportMarkers walks a session's events and returns a map of
// path -> title for every persisted report-marker record. Used at session-
// detail load time to project the IsReport / ReportTitle fields back onto
// reconstructed ArtifactInfo entries.
func collectReportMarkers(events session.Events) map[string]string {
	markers := make(map[string]string)
	for i := range events.Len() {
		event := events.At(i)
		if event.LLMResponse.Content == nil {
			continue
		}
		for _, part := range event.LLMResponse.Content.Parts {
			if part.Text == "" {
				continue
			}
			if !strings.HasPrefix(part.Text, reportMarkerPrefix) {
				continue
			}
			jsonStr := part.Text[len(reportMarkerPrefix):]
			var payload reportMarkerPayload
			if err := json.Unmarshal([]byte(jsonStr), &payload); err != nil {
				continue
			}
			if payload.Path == "" {
				continue
			}
			// Last write wins for a given path — if the agent re-emits a
			// marker for the same file across turns, the most recent title
			// is kept.
			markers[payload.Path] = payload.Title
		}
	}
	return markers
}

// joinReportMarkers projects a map of report markers (as returned by
// collectReportMarkers) onto a slice of ArtifactInfo. Each artifact whose
// path is keyed in the markers map has IsReport flipped to true and
// ReportTitle filled with the marker's title (which may be empty). All
// other artifacts are returned unchanged.
func joinReportMarkers(artifacts []ArtifactInfo, markers map[string]string) []ArtifactInfo {
	if len(markers) == 0 || len(artifacts) == 0 {
		return artifacts
	}
	out := make([]ArtifactInfo, len(artifacts))
	copy(out, artifacts)
	for i := range out {
		if title, ok := markers[out[i].Path]; ok {
			out[i].IsReport = true
			out[i].ReportTitle = title
		}
	}
	return out
}

// tryParseReportMarkerMessage reports whether the given text is a persisted
// report-marker record. Unlike app_preview / distill markers, report markers
// do NOT surface as their own chat bubble — their effect is purely a flag
// projected onto the corresponding artifact's ArtifactInfo by joinReportMarkers.
// The playback loop calls this helper to detect and skip such records so they
// are neither rendered nor coalesced into the agent prose around them.
func tryParseReportMarkerMessage(text string) bool {
	return strings.HasPrefix(text, reportMarkerPrefix)
}

// --- Flow output persistence ---
//
// Flow output messages are persisted using the same prefix-marker pattern as
// distill and app_preview. When run_flow produces large output (>500 chars),
// the output is stripped from the tool result (to save LLM context) and
// delivered to the user via a transient SSE event. We persist it here so
// the output survives page refresh / session reload.
// Format: [flow_output]<raw output text>

const flowOutputPrefix = "[flow_output]"

// persistFlowOutput saves the flow output to the session transcript so it
// can be reconstructed on session reload.
func persistFlowOutput(ctx context.Context, svc session.Service, userID, sessionID, content string) {
	if svc == nil || sessionID == "" || content == "" {
		return
	}
	persistSessionMessage(ctx, svc, userID, sessionID, "model", flowOutputPrefix+content)
}

// tryParseFlowOutputMessage checks if a text starts with the flow_output marker
// prefix and returns a structured StudioMessage if so. Returns nil if not a
// flow output message.
func tryParseFlowOutputMessage(text string) *StudioMessage {
	if !strings.HasPrefix(text, flowOutputPrefix) {
		return nil
	}
	content := text[len(flowOutputPrefix):]
	return &StudioMessage{
		Type:    "flow_output",
		Content: content,
	}
}
