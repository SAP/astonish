package agent

import (
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/credentials"
	"iter"
	"reflect"
	"strings"
	"sync"
	"time"

	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

const (
	turnContextStateKey  = "_astonish_turn_context"
	systemPromptStateKey = "_astonish_system_prompt"
)

type knowledgeCaptureStatus string

const (
	knowledgeSnapshotVersion                        = 1
	knowledgeCaptureCaptured knowledgeCaptureStatus = "captured"
	knowledgeCaptureDisabled knowledgeCaptureStatus = "disabled"
	automaticMemoryPrefix                           = "[Astonish Automatic Memory — current invocation only, not user-authored]\n\n"
	legacyTurnContextPrefix                         = "[Astonish Per-Turn Context — not user-authored]\n\n"
)

// turnKnowledgeSnapshot is invocation-local. Strings and copied results are never
// handed to mutable request/event consumers; capture and injection share these bytes.
type turnKnowledgeSnapshot struct {
	text          string
	results       []KnowledgeSearchResult
	query         string
	bm25QueryLen  int
	captureStatus knowledgeCaptureStatus
	redacted      bool
}

func buildTurnKnowledgeSnapshot(query, bm25Query string, results []KnowledgeSearchResult, redactor *credentials.Redactor, capture bool) turnKnowledgeSnapshot {
	s := turnKnowledgeSnapshot{query: query, bm25QueryLen: len(bm25Query), captureStatus: knowledgeCaptureDisabled}
	if capture {
		s.captureStatus = knowledgeCaptureCaptured
	}
	sanitize := func(text string) string {
		if redactor == nil {
			return text
		}
		clean := redactor.Redact(text)
		s.redacted = s.redacted || clean != text
		return clean
	}
	s.query = sanitize(query)
	var body strings.Builder
	for _, result := range deduplicateSearchResults(results) {
		r := result
		r.ID = sanitize(r.ID)
		r.Path = EscapeCurlyPlaceholders(sanitize(r.Path))
		r.Snippet = EscapeCurlyPlaceholders(sanitize(r.Snippet))
		r.Category = sanitize(r.Category)
		r.Scope = sanitize(r.Scope)
		r.CreatedBy = sanitize(r.CreatedBy)
		r.CreatedAt = sanitize(r.CreatedAt)
		r.SessionID = sanitize(r.SessionID)
		s.results = append(s.results, r)
		fmt.Fprintf(&body, "**%s** (relevance: %.0f%%)\n%s\n\n", r.Path, r.Score*100, r.Snippet)
	}
	if len(s.results) > 0 {
		s.text = sanitize(automaticMemoryPrefix + "## Knowledge For This Task\n\n" + platformKnowledgeGuidance + body.String())
	}
	return s
}

func (s turnKnowledgeSnapshot) estimatedTokens() int { return len(s.text) / 3 }

// turnKnowledgeLLM assembles the final provider request after all history callbacks.
// It never modifies the ADK request, and every call gets its own suffix object.
type turnKnowledgeLLM struct {
	model.LLM
	snapshot   turnKnowledgeSnapshot
	onDispatch func()
	once       sync.Once
}

func (l *turnKnowledgeLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		copy := *req
		copy.Contents = append([]*genai.Content(nil), req.Contents...)
		if l.snapshot.text != "" {
			copy.Contents = append(copy.Contents, genai.NewContentFromText(l.snapshot.text, genai.RoleUser))
		}
		l.once.Do(func() {
			if l.onDispatch != nil {
				l.onDispatch()
			}
		})
		for response, err := range l.LLM.GenerateContent(ctx, &copy, stream) {
			if !yield(response, err) {
				return
			}
		}
	}
}

// projectLegacyKnowledge only edits exact copies of proven hidden native events.
// Prefix-only user lookalikes are not provenance. Ambiguous repeated boundaries
// or unmarked legacy imports are preserved rather than deleting user instructions.
func projectLegacyKnowledge(contents []*genai.Content, events session.Events) []*genai.Content {
	if events == nil {
		return contents
	}
	var proven, unproven []*genai.Content
	for i := 0; i < events.Len(); i++ {
		e := events.At(i)
		if e == nil || e.Content == nil || e.Author != "user" {
			continue
		}
		marked, _ := e.Actions.StateDelta[turnContextStateKey].(bool)
		if !IsTurnContextContent(e.Content) {
			continue
		}
		if marked || strings.HasPrefix(e.ID, "turn-context-") || strings.HasPrefix(e.ID, "compact-") {
			proven = append(proven, e.Content)
		} else {
			unproven = append(unproven, e.Content)
		}
	}
	var out []*genai.Content
	changed := false
	for _, content := range contents {
		projected := content
		ambiguous := false
		for _, original := range unproven {
			if content == original || reflect.DeepEqual(content, original) {
				ambiguous = true
				break
			}
		}
		if !ambiguous {
			for _, original := range proven {
				if content == original || reflect.DeepEqual(content, original) {
					projected = projectLegacyKnowledgeContent(content)
					break
				}
			}
		}
		changed = changed || projected != content
		if projected != nil {
			out = append(out, projected)
		}
	}
	if !changed {
		return contents
	}
	return out
}

func projectLegacyKnowledgeContent(content *genai.Content) *genai.Content {
	if content == nil || content.Role != "user" {
		return content
	}
	const boundary = "\n\n## Knowledge For This Task\n\n"
	copy := *content
	copy.Parts = nil
	changed := false
	for _, part := range content.Parts {
		if part == nil || !strings.HasPrefix(part.Text, legacyTurnContextPrefix) || strings.Count(part.Text, boundary) != 1 {
			copy.Parts = append(copy.Parts, part)
			continue
		}
		idx := strings.Index(part.Text, boundary)
		body := part.Text[idx+len(boundary):]
		// A native knowledge section starts with its known policy. A heading
		// embedded in arbitrary earlier context is not sufficient evidence.
		if !strings.HasPrefix(body, platformKnowledgeGuidance) && !strings.HasPrefix(body, "CRITICAL — You MUST apply the following knowledge when executing this task.") {
			copy.Parts = append(copy.Parts, part)
			continue
		}
		p := *part
		p.Text = part.Text[:idx]
		changed = true
		if p.Text != strings.TrimSuffix(legacyTurnContextPrefix, "\n\n") {
			copy.Parts = append(copy.Parts, &p)
		}
	}
	if !changed {
		return content
	}
	for _, part := range copy.Parts {
		if part != nil {
			return &copy
		}
	}
	return nil
}

// RenderExternalTurnContext renders the model-facing dynamic context without
// creating an ADK event. Public MCP returns this exact text to its external
// client, while native chat persists it through buildTurnContextContent.
func RenderExternalTurnContext(overrides *PromptOverrides, relevantTools, relevantKnowledge string) string {
	content := buildTurnContextContent(overrides, relevantTools, relevantKnowledge)
	if content == nil || len(content.Parts) == 0 || content.Parts[0] == nil {
		return ""
	}
	return content.Parts[0].Text
}

// buildTurnContextContent constructs the exact per-turn context persisted beside
// the clean user message. ADK replays this user-role event on every later call.
func buildTurnContextContent(overrides *PromptOverrides, relevantTools, relevantKnowledge string) *genai.Content {
	return buildTurnContextContentForMode(overrides, relevantTools, relevantKnowledge, false)
}

// projectCompletedCodePlanRequest changes only provider copies of proven hidden
// context. Stored events and user-authored lookalikes remain untouched.
func projectCompletedCodePlanRequest(ctx context.Context, req *model.LLMRequest, s PlanSnapshot) {
	if req == nil {
		return
	}
	reference := CompletedCodePlanReference(s, "session PLAN.md")
	if path, _ := ctx.Value(delegationPlanSourceKey{}).(string); path != "" {
		reference = CompletedCodePlanReference(s, path)
	}
	if req.Config != nil && req.Config.SystemInstruction != nil {
		cfg := *req.Config
		content := *cfg.SystemInstruction
		content.Parts = nil
		for _, p := range cfg.SystemInstruction.Parts {
			if p != nil && (strings.HasPrefix(p.Text, "\nCode execution runtime:") || strings.HasPrefix(p.Text, "The user is reviewing phase ") || strings.HasPrefix(p.Text, "The previous response ended before the approved work")) {
				continue
			}
			content.Parts = append(content.Parts, p)
		}
		cfg.SystemInstruction = &content
		req.Config = &cfg
	}
	// CallbackContext embeds context but only invocation contexts expose Session.
	type sessionContext interface{ Session() session.Session }
	sc, ok := ctx.(sessionContext)
	if !ok || sc.Session() == nil {
		return
	}
	contents := append([]*genai.Content(nil), req.Contents...)
	for i, c := range contents {
		proven, ambiguous := false, false
		for e := range sc.Session().Events().All() {
			if e == nil || e.Content == nil || !(c == e.Content || reflect.DeepEqual(c, e.Content)) {
				continue
			}
			marked, _ := e.Actions.StateDelta[turnContextStateKey].(bool)
			if e.Author == "user" && IsTurnContextContent(e.Content) && (marked || strings.HasPrefix(e.ID, "turn-context-") || strings.HasPrefix(e.ID, "compact-")) {
				proven = true
			} else {
				ambiguous = true
			}
		}
		if !proven || ambiguous {
			continue
		}
		copy := *c
		copy.Parts = nil
		for _, part := range c.Parts {
			if part == nil {
				copy.Parts = append(copy.Parts, part)
				continue
			}
			p := *part
			// Session Task is the generated full plan, followed by the native Runtime
			// Mode boundary. Keep unrelated sections before and after these two.
			start := strings.Index(p.Text, "## Session Task\n\n")
			mode := strings.Index(p.Text, "\n\n## Runtime Mode\n\n")
			end := mode
			if start >= 0 {
				if skill := strings.Index(p.Text[start:], "\n\n## Available Skills For This Request\n\n"); skill >= 0 && (end < 0 || start+skill < end) {
					end = start + skill
				}
			}
			if start >= 0 && end > start {
				body := p.Text[start+len("## Session Task\n\n") : end]
				activePrefix, _, _ := strings.Cut(PlanExecutionSystemContext, "__PLAN_BODY__")
				completedPrefix, _, _ := strings.Cut(PlanCompletedSystemContext, "__PLAN_BODY__")
				if strings.HasPrefix(body, activePrefix) || strings.HasPrefix(body, completedPrefix) || strings.HasPrefix(body, completedCodePlanPrefix) {
					p.Text = p.Text[:start] + "## Session Task\n\n" + reference + p.Text[end:]
				}
			}
			p.Text = strings.ReplaceAll(p.Text, "An approved plan is being executed. Follow it without replacing it.", "The earlier plan is historical. Ordinary Normal mode is active.")
			copy.Parts = append(copy.Parts, &p)
		}
		contents[i] = &copy
	}
	req.Contents = contents
}

const completedCodePlanPrefix = "Completed Code plan (historical reference only): "

// CompletedCodePlanReference keeps identity and location, not execution instructions.
func CompletedCodePlanReference(s PlanSnapshot, path string) string {
	return fmt.Sprintf("%s%q (plan_id=%s, revision=%d). History: %s. Ordinary Normal mode is active; this plan does not constrain new work or delegation.", completedCodePlanPrefix, s.Goal, s.PlanID, s.Revision, path)
}

func buildTurnContextContentForMode(overrides *PromptOverrides, relevantTools, relevantKnowledge string, codeMode bool) *genai.Content {
	var sections []string
	add := func(title, body string) {
		if strings.TrimSpace(body) != "" {
			sections = append(sections, "## "+title+"\n\n"+body)
		}
	}

	if overrides != nil {
		add("Output Constraints", overrides.ChannelHints)
		// Bot execution hints/persona belong only to the cloned system prompt.
		// Do not persist them as user-role events, including external rendering.
		if overrides.Bot == nil {
			add("Execution Context", overrides.SchedulerHints)
			add("Session Task", overrides.SessionContext)
		}
		add("Available Skills For This Request", overrides.SkillIndex)
		var modes []string
		if overrides.PlanMode {
			modes = append(modes, "Plan mode is active. Mutating tools are blocked by the runtime.")
		}
		if overrides.GraphPlanMode {
			modes = append(modes, "Graph-optimized Plan mode is active. The runtime enforces its phased tool gate.")
		}
		if overrides.AskMode {
			modes = append(modes, "Ask mode is active. Mutating tools are blocked by the runtime.")
		}
		if overrides.ApprovedPlanCompleted {
			modes = append(modes, "An approved plan was executed and completed earlier in this session. You are in normal conversation mode — not execution mode.")
		} else if overrides.ApprovedPlanExecution {
			modes = append(modes, "An approved plan is being executed. Follow it without replacing it.")
		}
		add("Runtime Mode", strings.Join(modes, "\n"))
	}
	if relevantTools != "" {
		add("Relevant Tools For This Request", "These catalog tools may be useful. Call `describe_tools` for their schemas, then invoke them through `execute_tool`. Use `search_tools` if you need additional tools.\n\n"+relevantTools)
	}
	if relevantKnowledge != "" {
		guidance := platformKnowledgeGuidance
		if codeMode {
			guidance = "CRITICAL — You MUST apply the following knowledge when executing this task. It contains proven commands, specific flags, and workarounds that are KNOWN TO WORK from previous sessions. Use the exact commands and approaches described here.\n\n"
		}
		add("Knowledge For This Task", guidance+relevantKnowledge)
	}
	if len(sections) == 0 {
		return nil
	}
	return genai.NewContentFromText("[Astonish Per-Turn Context — not user-authored]\n\n"+strings.Join(sections, "\n\n"), genai.RoleUser)
}

func newTurnContextEvent(content *genai.Content) *session.Event {
	if content == nil {
		return nil
	}
	return &session.Event{
		ID:          fmt.Sprintf("turn-context-%d", time.Now().UnixNano()),
		Author:      "user",
		Timestamp:   time.Now(),
		LLMResponse: model.LLMResponse{Content: content},
		Actions: session.EventActions{StateDelta: map[string]any{
			turnContextStateKey: true,
		}},
	}
}

func stableSystemPrompt(state session.State, build func() string) (string, *session.Event) {
	if saved, err := state.Get(systemPromptStateKey); err == nil {
		if prompt, ok := saved.(string); ok && prompt != "" {
			return prompt, nil
		}
	}
	prompt := build()
	return prompt, newSystemPromptStateEvent(prompt)
}

func newSystemPromptStateEvent(prompt string) *session.Event {
	return &session.Event{
		ID:        fmt.Sprintf("system-prompt-%d", time.Now().UnixNano()),
		Author:    "system",
		Timestamp: time.Now(),
		Actions: session.EventActions{StateDelta: map[string]any{
			systemPromptStateKey: prompt,
		}},
	}
}

// IsTurnContextContent reports whether content is hidden model-facing turn context.
func IsTurnContextContent(content *genai.Content) bool {
	if content == nil {
		return false
	}
	for _, part := range content.Parts {
		if part != nil && strings.HasPrefix(part.Text, "[Astonish Per-Turn Context — not user-authored]\n") {
			return true
		}
	}
	return false
}

// MarkTurnContextEvent preserves hidden context semantics on a rewritten event.
func MarkTurnContextEvent(event *session.Event) {
	if event == nil {
		return
	}
	if event.Actions.StateDelta == nil {
		event.Actions.StateDelta = make(map[string]any)
	}
	event.Actions.StateDelta[turnContextStateKey] = true
}

// IsTurnContextEvent reports whether an event is hidden model-facing turn context.
func IsTurnContextEvent(event *session.Event) bool {
	if event == nil {
		return false
	}
	if event.Actions.StateDelta != nil {
		marked, _ := event.Actions.StateDelta[turnContextStateKey].(bool)
		if marked {
			return true
		}
	}
	return IsTurnContextContent(event.LLMResponse.Content)
}
