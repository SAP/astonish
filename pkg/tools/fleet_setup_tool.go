package tools

import (
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/fleet"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// UpdateSetupDraftArgs merges step values into a setup draft.
type UpdateSetupDraftArgs struct {
	DraftID  string         `json:"draft_id" jsonschema:"Setup draft UUID from the fleet setup wizard"`
	StepID   string         `json:"step_id" jsonschema:"Step ID being updated (e.g., provisioning, channel)"`
	Values   map[string]any `json:"values" jsonschema:"Field values for this step. Info steps are acknowledged with {\"_ack\": true}."`
	MarkStep string         `json:"current_step,omitempty" jsonschema:"Optional current step pointer after update"`
}

// UpdateSetupDraftResult is returned by update_setup_draft.
type UpdateSetupDraftResult struct {
	Status       string   `json:"status"`
	Message      string   `json:"message"`
	DraftID      string   `json:"draft_id,omitempty"`
	StepComplete bool     `json:"step_complete,omitempty"`
	NextStep     string   `json:"next_step,omitempty"`
	Errors       []string `json:"errors,omitempty"`
}

func updateSetupDraft(tc tool.Context, args UpdateSetupDraftArgs) (UpdateSetupDraftResult, error) {
	draftStore := setupDraftStoreFromContext(tc)
	if draftStore == nil {
		return UpdateSetupDraftResult{Status: "error", Message: "Setup draft store not available"}, nil
	}
	draftID := strings.TrimSpace(args.DraftID)
	if draftID == "" {
		return UpdateSetupDraftResult{Status: "error", Message: "draft_id is required"}, nil
	}
	draft, ok := draftStore.Get(tc, draftID)
	if !ok {
		return UpdateSetupDraftResult{Status: "error", Message: "Draft not found"}, nil
	}
	if draft.Collected == nil {
		draft.Collected = map[string]any{}
	}
	stepID := strings.TrimSpace(args.StepID)
	if stepID == "" {
		return UpdateSetupDraftResult{Status: "error", Message: "step_id is required"}, nil
	}
	existing, _ := draft.Collected[stepID].(map[string]any)
	if existing == nil {
		existing = map[string]any{}
	}
	for k, v := range args.Values {
		existing[k] = v
	}
	draft.Collected[stepID] = existing
	if args.MarkStep != "" {
		draft.CurrentStep = args.MarkStep
	} else {
		draft.CurrentStep = stepID
	}

	profileStore := setupProfileStoreFromContext(tc)
	profile, err := fleet.ResolveSetupProfile(tc, draft.SetupProfileKey, profileStore)
	if err != nil {
		return UpdateSetupDraftResult{Status: "error", Message: err.Error()}, nil
	}
	normalizeInfoStepAck(profile, stepID, existing)
	engine := fleet.NewSetupEngine(nil)
	collected := fleet.ParseSetupCollected(draft.Collected)
	if valErr := engine.ValidateStep(profile, stepID, collected); valErr != nil {
		return UpdateSetupDraftResult{
			Status:  "validation_error",
			Message: valErr.Error(),
			DraftID: draftID,
			Errors:  []string{valErr.Error()},
		}, nil
	}

	if err := draftStore.Update(tc, draft); err != nil {
		return UpdateSetupDraftResult{Status: "error", Message: err.Error()}, nil
	}

	next := engine.NextIncompleteStep(profile, collected)
	return UpdateSetupDraftResult{
		Status:       "ok",
		Message:      "Draft updated",
		DraftID:      draftID,
		StepComplete: true,
		NextStep:     next,
	}, nil
}

// GetSetupProfileArgs loads a setup profile and completion status.
type GetSetupProfileArgs struct {
	ProfileKey string `json:"profile_key" jsonschema:"Setup profile key (e.g., software-development, generic)"`
	DraftID    string `json:"draft_id,omitempty" jsonschema:"Optional draft ID to include completion status"`
}

// GetSetupProfileResult is returned by get_setup_profile.
type GetSetupProfileResult struct {
	Status           string                       `json:"status"`
	ProfileKey       string                       `json:"profile_key,omitempty"`
	Name             string                       `json:"name,omitempty"`
	Steps            []fleet.SetupStep            `json:"steps,omitempty"`
	Completion       []fleet.StepCompletionStatus `json:"completion,omitempty"`
	CurrentStep      string                       `json:"current_step,omitempty"`
	CurrentStepTitle string                       `json:"current_step_title,omitempty"`
	StepPrompt       string                       `json:"step_prompt,omitempty"`
	StepTools        []string                     `json:"step_tools,omitempty"`
	StepToolGroups   []string                     `json:"step_tool_groups,omitempty"`
	NextStep         string                       `json:"next_incomplete_step,omitempty"`
	Message          string                       `json:"message,omitempty"`
}

func getSetupProfile(tc tool.Context, args GetSetupProfileArgs) (GetSetupProfileResult, error) {
	key := strings.TrimSpace(args.ProfileKey)
	if key == "" {
		key = fleet.DefaultSetupProfileKey
	}
	profileStore := setupProfileStoreFromContext(tc)
	profile, err := fleet.ResolveSetupProfile(tc, key, profileStore)
	if err != nil {
		return GetSetupProfileResult{Status: "error", Message: err.Error()}, nil
	}
	engine := fleet.NewSetupEngine(nil)
	result := GetSetupProfileResult{
		Status:     "ok",
		ProfileKey: profile.Key,
		Name:       profile.Name,
		Steps:      profile.Steps,
	}

	var collected fleet.SetupCollected
	draftCtx := fleet.SetupDraftContext{}
	if draftID := strings.TrimSpace(args.DraftID); draftID != "" {
		if draftStore := setupDraftStoreFromContext(tc); draftStore != nil {
			if draft, ok := draftStore.Get(tc, draftID); ok {
				collected = fleet.ParseSetupCollected(draft.Collected)
				draftCtx.BaseFleetKey = draft.TemplateKey
			}
		}
	}
	result.Completion = engine.StepCompletion(profile, collected)
	result.NextStep = engine.NextIncompleteStep(profile, collected)
	if step, stepID := engine.CurrentStep(profile, collected); step != nil {
		result.CurrentStep = stepID
		result.CurrentStepTitle = step.Title
		result.StepPrompt = engine.ComposeStepPrompt(profile, stepID, collected, draftCtx)
		result.StepTools = fleet.StepTools(step, collected)
		result.StepToolGroups = fleet.StepToolGroups(profile, step, collected)
	}
	return result, nil
}

func normalizeInfoStepAck(profile *fleet.SetupProfile, stepID string, values map[string]any) {
	if profile == nil || values == nil {
		return
	}
	step, ok := profile.StepByID(stepID)
	if !ok || step.EffectiveType() != "info" {
		return
	}
	if isTruthyAck(values["_ack"]) {
		values["_ack"] = true
		return
	}
	for _, key := range []string{
		"ack",
		"acknowledged",
		"acknowledge",
		"confirmed",
		"user_confirmed",
		"overview_acknowledged",
		"overview_ack",
		"proceed",
	} {
		if isTruthyAck(values[key]) {
			values["_ack"] = true
			return
		}
	}
	if status, ok := values["status"].(string); ok && strings.EqualFold(strings.TrimSpace(status), "acknowledged") {
		values["_ack"] = true
	}
}

func isTruthyAck(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		s := strings.TrimSpace(strings.ToLower(x))
		return s == "true" || s == "yes" || s == "y" || s == "acknowledged" || s == "confirmed"
	default:
		return false
	}
}

func setupDraftStoreFromContext(tc tool.Context) store.FleetSetupDraftStore {
	if fs := store.FleetSetupDraftStoreFromContext(tc); fs != nil {
		return fs
	}
	if svc := store.FromContext(tc); svc != nil && svc.FleetSetupDrafts != nil {
		return svc.FleetSetupDrafts
	}
	return nil
}

func setupProfileStoreFromContext(tc tool.Context) store.FleetSetupProfileStore {
	if fs := store.FleetSetupProfileStoreFromContext(tc); fs != nil {
		return fs
	}
	if svc := store.FromContext(tc); svc != nil && svc.FleetSetupProfiles != nil {
		return svc.FleetSetupProfiles
	}
	return nil
}

// GetFleetSetupTools returns setup profile helper tools for plan creation wizards.
func GetFleetSetupTools() ([]tool.Tool, error) {
	updateTool, err := functiontool.New(functiontool.Config{
		Name: "update_setup_draft",
		Description: "Merge collected setup values into an in-progress fleet setup draft. " +
			"Validates the step before saving. For info steps, acknowledge with values {\"_ack\": true}. " +
			"Returns next_step when the step is complete. " +
			"Use during plan creation to persist step outputs, especially provisioning (template, container_workspace_dir).",
	}, updateSetupDraft)
	if err != nil {
		return nil, fmt.Errorf("update_setup_draft: %w", err)
	}
	getTool, err := functiontool.New(functiontool.Config{
		Name:        "get_setup_profile",
		Description: "Load a fleet setup profile definition, current step prompt, tools, and draft completion status.",
	}, getSetupProfile)
	if err != nil {
		return nil, fmt.Errorf("get_setup_profile: %w", err)
	}
	return []tool.Tool{updateTool, getTool}, nil
}
