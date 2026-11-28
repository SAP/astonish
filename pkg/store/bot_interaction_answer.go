package store

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/question"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// BotInteractionAnswerMessage constructs the same correlated owner input for
// transactional acceptance and worker resume. A previously accepted canonical
// row is validated against its source, not mistaken for a second owner turn.
func BotInteractionAnswerMessage(events []*session.Event, approval *BotApproval) (*genai.Content, error) {
	if approval == nil || approval.Interaction == nil || approval.Interaction.ValidateResponse(approval.Response) != nil {
		return nil, ErrBotInvalid
	}
	var accepted *session.Event
	source := make([]*session.Event, 0, len(events))
	for _, e := range events {
		if e != nil && e.ID == "bot-owner-answer:"+approval.ID.String() {
			if accepted != nil {
				return nil, fmt.Errorf("duplicate accepted owner answer: %w", ErrBotConflict)
			}
			accepted = e
			continue
		}
		source = append(source, e)
	}
	var content *genai.Content
	if approval.Interaction.Kind == BotInteractionPlanDefinition {
		definition, err := approval.Interaction.PlanDefinition()
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(map[string]any{"type": "bot_plan_execution_consent", "approval_id": approval.ID, "definition": definition})
		if err != nil {
			return nil, err
		}
		content = genai.NewContentFromText(string(raw), "user")
	} else {
		var payload map[string]any
		if err := json.Unmarshal(approval.Interaction.Payload, &payload); err != nil {
			return nil, err
		}
		var lastCall *genai.FunctionCall
		var callInvocation, matchedCall, matchedInvocation string
		matches := 0
		for _, e := range source {
			if e == nil || e.Content == nil || e.Partial {
				continue
			}
			if matches > 0 && e.Author == "user" {
				return nil, fmt.Errorf("user event followed matched question response: %w", ErrBotConflict)
			}
			for _, p := range e.Content.Parts {
				if p == nil {
					continue
				}
				if p.FunctionCall != nil {
					lastCall, callInvocation = p.FunctionCall, e.InvocationID
				}
				r := p.FunctionResponse
				if r == nil || r.Response["questionId"] != approval.Interaction.QuestionID {
					continue
				}
				if lastCall == nil || lastCall.ID == "" || r.ID != lastCall.ID || r.Name != lastCall.Name || e.InvocationID != callInvocation || r.Response["status"] != "ok" {
					return nil, fmt.Errorf("question call/response correlation mismatch: %w", ErrBotConflict)
				}
				target := lastCall.Name
				if target == common.ExecuteToolName {
					var err error
					target, _, err = common.NormalizeExecuteToolArgs(lastCall.Args)
					if err != nil {
						return nil, ErrBotConflict
					}
				}
				if target != "ask_user" {
					return nil, fmt.Errorf("matched response belongs to non-question tool: %w", ErrBotConflict)
				}
				raw, err := json.Marshal(r.Response)
				if err != nil {
					return nil, err
				}
				var response map[string]any
				if err := json.Unmarshal(raw, &response); err != nil {
					return nil, err
				}
				for key, value := range payload {
					if !reflect.DeepEqual(value, response[key]) {
						return nil, fmt.Errorf("question payload/response mismatch at %s: %w", key, ErrBotConflict)
					}
				}
				matches++
				matchedCall, matchedInvocation = r.ID, e.InvocationID
			}
		}
		if matches != 1 || lastCall == nil || lastCall.ID != matchedCall || callInvocation != matchedInvocation {
			return nil, fmt.Errorf("question response count or final-call mismatch matches=%d: %w", matches, ErrBotConflict)
		}
		var q question.Question
		if err := json.Unmarshal(approval.Interaction.Payload, &q); err != nil {
			return nil, err
		}
		label := approval.Response.Answer
		for _, option := range q.Options {
			if option.ID == label && option.Label != "" {
				label = option.Label
				break
			}
		}
		raw, err := json.Marshal(map[string]any{"type": "bot_owner_answer", "approval_id": approval.ID, "invocation_id": matchedInvocation, "tool_call_id": matchedCall, "tool": "ask_user", "response": approval.Response, "display": map[string]string{"prompt": q.Prompt, "answer": label}})
		if err != nil {
			return nil, err
		}
		content = genai.NewContentFromText(string(raw), "user")
	}
	if accepted != nil && (accepted.Partial || accepted.Author != "user" || !reflect.DeepEqual(accepted.Content, content)) {
		return nil, fmt.Errorf("accepted owner answer differs from reconstructed content: %w", ErrBotConflict)
	}
	return content, nil
}
