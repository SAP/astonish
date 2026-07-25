package fleet

import (
	"fmt"
	"strings"
)

const setupStepAckField = "_ack"

// StepCompletionStatus reports per-step completion for a profile and draft.
type StepCompletionStatus struct {
	StepID   string `json:"step_id"`
	Title    string `json:"title"`
	Type     string `json:"type"`
	Active   bool   `json:"active"`
	Complete bool   `json:"complete"`
}

// CurrentStep returns the first incomplete active step, or nil if all complete.
func (e *SetupEngine) CurrentStep(profile *SetupProfile, collected SetupCollected) (*SetupStep, string) {
	if profile == nil {
		return nil, ""
	}
	stepID := e.NextIncompleteStep(profile, collected)
	if stepID == "" {
		return nil, ""
	}
	step, ok := profile.StepByID(stepID)
	if !ok {
		return nil, ""
	}
	return step, stepID
}

// StepCompletion returns completion status for all steps.
func (e *SetupEngine) StepCompletion(profile *SetupProfile, collected SetupCollected) []StepCompletionStatus {
	if profile == nil {
		return nil
	}
	out := make([]StepCompletionStatus, 0, len(profile.Steps))
	for _, step := range profile.Steps {
		active := e.StepActive(&step, collected)
		complete := active && e.StepComplete(profile, step.ID, collected)
		out = append(out, StepCompletionStatus{
			StepID:   step.ID,
			Title:    step.Title,
			Type:     step.EffectiveType(),
			Active:   active,
			Complete: complete,
		})
	}
	return out
}

// ComposeSessionIntro returns the optional profile-level session preamble.
func (e *SetupEngine) ComposeSessionIntro(profile *SetupProfile) string {
	if profile == nil {
		return ""
	}
	if intro := strings.TrimSpace(profile.IntroPrompt); intro != "" {
		return intro
	}
	if desc := strings.TrimSpace(profile.Description); desc != "" {
		return desc
	}
	return fmt.Sprintf("You are guiding the user through fleet plan setup using profile %q.", profile.Key)
}

// ComposeStepPrompt builds LLM instructions for a single setup step.
func (e *SetupEngine) ComposeStepPrompt(profile *SetupProfile, stepID string, collected SetupCollected, draftCtx SetupDraftContext) string {
	if profile == nil {
		return ""
	}
	step, ok := profile.StepByID(stepID)
	if !ok {
		return ""
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("## Current step: %s\n\n", step.Title))
	if summary := strings.TrimSpace(step.Summary); summary != "" {
		b.WriteString(summary + "\n\n")
	}

	prompt := step.StepPromptText()
	if step.EffectiveType() == "agent_select" && len(draftCtx.TemplateAgentNames) > 0 {
		b.WriteString(fmt.Sprintf("Available agents: %s\n\n", strings.Join(draftCtx.TemplateAgentNames, ", ")))
	}
	if prompt != "" {
		b.WriteString(prompt)
		b.WriteString("\n\n")
	}

	if content := strings.TrimSpace(step.Content); content != "" && step.EffectiveType() == "info" {
		b.WriteString(content)
		b.WriteString("\n\n")
	}

	tools := StepTools(step, collected)
	if len(tools) > 0 {
		b.WriteString("Tools for this step: " + strings.Join(tools, ", ") + "\n\n")
	}

	b.WriteString(e.composeStrictRules(profile, stepID, collected))
	return b.String()
}

func (e *SetupEngine) composeStrictRules(profile *SetupProfile, stepID string, collected SetupCollected) string {
	var b strings.Builder
	b.WriteString("STRICT RULES:\n")
	b.WriteString("- Focus ONLY on the current step. Do not collect data for future steps.\n")
	b.WriteString("- When all required fields for this step are gathered, call update_setup_draft with step_id and values.\n")
	if profile != nil {
		if step, ok := profile.StepByID(stepID); ok && step.EffectiveType() == "info" {
			b.WriteString("- For this info step, after the user confirms, call update_setup_draft with values {\"_ack\": true}. No other acknowledgement key completes the step.\n")
		}
	}
	b.WriteString("- If validation fails or information is missing, ask follow-up questions. Do not advance.\n")
	b.WriteString("- Call get_setup_profile with draft_id to check completion state.\n")
	if stepID == "review" || profile != nil {
		if step, ok := profile.StepByID(stepID); ok && step.EffectiveType() == "review" {
			b.WriteString("- On review: call validate_fleet_plan then save_fleet_plan after validation passes.\n")
		}
	}
	return b.String()
}

// CollectedSummary returns a human-readable summary of collected values for LLM context.
func (e *SetupEngine) CollectedSummary(profile *SetupProfile, collected SetupCollected) string {
	if profile == nil || len(collected) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Progress so far\n\n")
	for _, step := range profile.Steps {
		if !e.StepActive(&step, collected) {
			continue
		}
		vals := collected[step.ID]
		if len(vals) == 0 {
			continue
		}
		if e.StepComplete(profile, step.ID, collected) {
			b.WriteString(fmt.Sprintf("- %s: complete\n", step.Title))
		} else {
			b.WriteString(fmt.Sprintf("- %s: in progress\n", step.Title))
		}
	}
	return b.String()
}

// ComposeWizardPrompt builds LLM system context scoped to the current incomplete step.
func (e *SetupEngine) ComposeWizardPrompt(profile *SetupProfile, templateKey string, collected SetupCollected) string {
	return e.ComposeWizardPromptWithContext(profile, templateKey, collected, SetupDraftContext{BaseFleetKey: templateKey})
}

// ComposeWizardPromptWithContext builds step-scoped chat prompt with optional draft context.
func (e *SetupEngine) ComposeWizardPromptWithContext(profile *SetupProfile, templateKey string, collected SetupCollected, draftCtx SetupDraftContext) string {
	if profile == nil {
		return ""
	}
	if draftCtx.BaseFleetKey == "" {
		draftCtx.BaseFleetKey = templateKey
	}

	// Deprecated wizard_prompt: log once via empty - callers may log; ignore content.
	_ = profile.WizardPrompt

	step, stepID := e.CurrentStep(profile, collected)
	if step == nil {
		// All steps complete — review/finalize guidance
		step, _ = profile.StepByID("review")
		if step == nil && len(profile.Steps) > 0 {
			last := profile.Steps[len(profile.Steps)-1]
			step = &last
			stepID = last.ID
		}
	}

	var b strings.Builder
	b.WriteString(e.ComposeSessionIntro(profile))
	b.WriteString("\n\n")
	if summary := e.CollectedSummary(profile, collected); summary != "" {
		b.WriteString(summary)
		b.WriteString("\n")
	}
	if step != nil {
		b.WriteString(e.ComposeStepPrompt(profile, stepID, collected, draftCtx))
	}
	return b.String()
}

// CurrentStepToolGroups returns pinned tool groups for the current incomplete step.
func (e *SetupEngine) CurrentStepToolGroups(profile *SetupProfile, collected SetupCollected) []string {
	step, _ := e.CurrentStep(profile, collected)
	if step == nil {
		if review, ok := profile.StepByID("review"); ok {
			step = review
		}
	}
	if step == nil {
		return profile.PinnedToolGroups
	}
	return StepToolGroups(profile, step, collected)
}

// SetupWizardContext is the resolved chat wizard state for a setup draft.
type SetupWizardContext struct {
	ProfileKey       string   `json:"profile_key"`
	Description      string   `json:"description,omitempty"`
	SystemPrompt     string   `json:"system_prompt"`
	PinnedToolGroups []string `json:"pinned_tool_groups,omitempty"`
	CurrentStepID    string   `json:"current_step_id,omitempty"`
	CurrentStepTitle string   `json:"current_step_title,omitempty"`
}

// BuildSetupWizardContext composes step-scoped wizard context for chat.
func BuildSetupWizardContext(profile *SetupProfile, collected SetupCollected, draftCtx SetupDraftContext) SetupWizardContext {
	if profile == nil {
		return SetupWizardContext{}
	}
	engine := NewSetupEngine(nil)
	out := SetupWizardContext{
		ProfileKey:       profile.Key,
		Description:      profile.Description,
		SystemPrompt:     engine.ComposeWizardPromptWithContext(profile, draftCtx.BaseFleetKey, collected, draftCtx),
		PinnedToolGroups: engine.CurrentStepToolGroups(profile, collected),
	}
	if step, stepID := engine.CurrentStep(profile, collected); step != nil {
		out.CurrentStepID = stepID
		out.CurrentStepTitle = step.Title
	}
	return out
}
