package agent

import (
	"fmt"
	"strings"
	"time"

	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

const (
	turnContextStateKey  = "_astonish_turn_context"
	systemPromptStateKey = "_astonish_system_prompt"
)

// buildTurnContextContent constructs the exact per-turn context persisted beside
// the clean user message. ADK replays this user-role event on every later call.
func buildTurnContextContent(overrides *PromptOverrides, relevantTools, relevantKnowledge string) *genai.Content {
	var sections []string
	add := func(title, body string) {
		if strings.TrimSpace(body) != "" {
			sections = append(sections, "## "+title+"\n\n"+body)
		}
	}

	if overrides != nil {
		add("Output Constraints", overrides.ChannelHints)
		add("Execution Context", overrides.SchedulerHints)
		add("Session Task", overrides.SessionContext)
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
		add("Knowledge For This Task", "CRITICAL — You MUST apply the following knowledge when executing this task. It contains proven commands, specific flags, and workarounds that are KNOWN TO WORK from previous sessions. Use the exact commands and approaches described here.\n\n"+relevantKnowledge)
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
