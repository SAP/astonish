package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// codePlanLoop is invocation-local, local-Code-only, and never fabricates user
// messages or re-enters ChatAgent.Run (which resets authorization grants).
type codePlanLoop struct {
	context       context.Context
	runtime       *PlanRuntime
	lastSequence  int64
	stagnant      int
	inputRequired bool
	calls         map[string]string
	reviewAtStart string
	reviewWorking bool
}

func newCodePlanLoop(ctx context.Context) *codePlanLoop {
	if po := PromptOverridesFromContext(ctx); po != nil && (po.PlanMode || po.GraphPlanMode || po.AskMode) {
		return &codePlanLoop{}
	}
	r, _ := PlanRuntimeFromContext(ctx)
	if r == nil || !r.options.CodeExecution {
		return &codePlanLoop{}
	}
	s := r.Snapshot()
	return &codePlanLoop{context: ctx, runtime: r, lastSequence: s.Sequence, calls: map[string]string{}, reviewAtStart: s.Document.CodeReviewPending}
}

func (l *codePlanLoop) active() bool {
	return l.runtime.CodeExecutionActive()
}

func (l *codePlanLoop) beforeModel(ctx adkagent.CallbackContext, req *model.LLMRequest) (*model.LLMResponse, error) {
	if l.runtime != nil && l.runtime.Snapshot().IsFullyAccepted() {
		projectCompletedCodePlanRequest(l.context, req, l.runtime.Snapshot())
	}
	if !l.active() {
		return nil, nil
	}
	snapshot, _ := json.Marshal(l.runtime.Snapshot())
	if req.Config == nil {
		req.Config = &genai.GenerateContentConfig{}
	}
	cfg := *req.Config
	parts := []*genai.Part{}
	if cfg.SystemInstruction != nil {
		parts = append(parts, cfg.SystemInstruction.Parts...)
	}
	parts = append(parts, &genai.Part{Text: "\nCode execution runtime: " + CodePlanRecoveryGuidance + "\nCurrent authoritative snapshot: " + string(snapshot)})
	if l.reviewAtStart != "" {
		parts = append(parts, &genai.Part{Text: "The user is reviewing phase " + l.reviewAtStart + ". Answer their question or make requested corrections to this phase, revising its definition if necessary and reverifying. Do not start a later phase until the user explicitly continues. A correction is not permission to advance."})
	}
	if l.stagnant > 0 {
		parts = append(parts, &genai.Part{Text: "The previous response ended before the approved work was complete. Do not ask permission to continue or stop for a progress report. Execute the next useful tool, finish prerequisites, or revise invalid acceptance with update_plan. Use ask_user only for actual necessary user-only information."})
	}
	cfg.SystemInstruction = &genai.Content{Role: "system", Parts: parts}
	req.Config = &cfg
	return nil, nil
}

func (l *codePlanLoop) beforeTool(_ tool.Context, _ tool.Tool, _ map[string]any) (map[string]any, error) {
	if l.runtime != nil && l.runtime.CodeReviewPending() && l.reviewAtStart == "" {
		return map[string]any{"status": "paused", "code": CodePlanReviewRequired, "message": "Verified phase awaits user review; continue in a new user turn."}, nil
	}
	return nil, nil
}

func (l *codePlanLoop) observe(event *session.Event) {
	if l.calls == nil {
		l.calls = map[string]string{}
	}
	if codePlanInputRequired(event, l.calls) {
		l.inputRequired = true
	}
	if l.runtime != nil && l.reviewAtStart != "" {
		for _, step := range l.runtime.Snapshot().Steps {
			if step.Name == l.reviewAtStart && step.Status != "complete" {
				l.reviewWorking = true
			}
		}
	}
}

func (l *codePlanLoop) checkpoint() string {
	if l.runtime == nil || !l.runtime.CodeReviewPending() {
		return ""
	}
	s := l.runtime.Snapshot()
	name := s.Document.CodeReviewPending
	// A new review turn may inspect/correct the just-finished phase. Do not end
	// it before its tools run. Reverification reinstates the same review boundary.
	if name == l.reviewAtStart && !l.reviewWorking {
		return ""
	}
	for _, step := range s.Steps {
		if step.Name == name {
			if step.Status != "complete" {
				return ""
			}
			return fmt.Sprintf("Phase %s verified. %s\nEvidence: %s\nPaused for review. Say continue to begin the next phase.", name, step.Outcome, step.Evidence)
		}
	}
	return "Verified phase paused for review. Say continue to proceed."
}

func (l *codePlanLoop) resume(ctx context.Context) (bool, error) {
	if !l.active() || l.inputRequired {
		return false, nil
	}
	if l.runtime.CodeReviewPending() {
		if l.reviewAtStart == "" || !l.reviewWorking {
			return false, nil
		}
		for _, step := range l.runtime.Snapshot().Steps {
			if step.Name == l.reviewAtStart && step.Status == "complete" {
				return false, nil
			}
		}
	}
	s := l.runtime.Snapshot()
	if s.Sequence == l.lastSequence {
		l.stagnant = min(l.stagnant+1, 5)
	} else {
		l.stagnant = 0
		l.lastSequence = s.Sequence
	}
	// Throttle repeated narration, never convert recoverable controls into fatal
	// stops. Cancellation is honored during the wait as well as model/tool calls.
	timer := time.NewTimer(time.Duration(1<<l.stagnant) * 100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-timer.C:
		return true, nil
	}
}
