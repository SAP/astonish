package api

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
)

// botWorkResponseSchema is an explicit JSON Schema, rather than a MIME hint.
// All object properties are required for strict structured-output endpoints;
// nonexcluded requirements use excluded:false and exclusion_reason:"".
func botWorkResponseSchema(prompt string, inputs ...[]byte) map[string]any {
	var state store.BotWorkState
	var contract store.BotWorkContract
	if len(inputs) > 0 {
		_ = json.Unmarshal(inputs[0], &state)
		_ = json.Unmarshal(inputs[0], &contract)
	}
	ids := []string{}
	for _, c := range state.Criteria {
		if !c.Excluded {
			ids = append(ids, c.ID)
		}
	}
	sources, _ := botWorkSourceReferences(contract)
	enum := func(values []string) map[string]any { return map[string]any{"type": "string", "enum": values} }
	text := func() map[string]any { return map[string]any{"type": "string"} }
	strings := func() map[string]any { return map[string]any{"type": "array", "items": text()} }
	object := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	var criterion map[string]any
	switch prompt {
	case botWorkPreparePrompt:
		criterion = object(map[string]any{"id": text(), "source_ids": map[string]any{"type": "array", "items": enum(sources), "minItems": 1}, "text": text(), "verification_kind": map[string]any{"type": "string", "enum": []string{string(store.BotWorkVerificationModel), string(store.BotWorkVerificationSourceURL), string(store.BotWorkVerificationRetrievalTime)}}, "fulfillment_scope": map[string]any{"type": "string", "enum": []string{string(store.BotWorkCriterionCurrentTurn), string(store.BotWorkCriterionFutureTaskDefinition)}}, "excluded": map[string]any{"type": "boolean"}, "exclusion_reason": text()}, "id", "source_ids", "text", "verification_kind", "fulfillment_scope", "excluded", "exclusion_reason")
		return object(map[string]any{"criteria": map[string]any{"type": "array", "items": criterion, "maxItems": 64}}, "criteria")
	case botWorkPendingReviewPrompt:
		review := botWorkResponseSchema(botWorkReviewPrompt, inputs...)
		return object(map[string]any{"current_turn": review, "deferred_application": review}, "current_turn", "deferred_application")
	case botWorkReviewPrompt:
		criterion = object(map[string]any{
			"criterion_id":  text(),
			"verdict":       map[string]any{"type": "string", "enum": []string{string(store.BotCriterionSatisfied), string(store.BotCriterionFailed), string(store.BotCriterionUnverified)}},
			"method":        map[string]any{"type": "string", "enum": []string{string(store.BotAssessmentModel)}},
			"evidence_refs": strings(), "explanation": text(),
		}, "criterion_id", "verdict", "method", "evidence_refs", "explanation")
		digest := text()
		if state.Candidate != nil {
			digest = enum([]string{state.Candidate.Digest})
			props := criterion["properties"].(map[string]any)
			props["criterion_id"] = enum(ids)
			props["evidence_refs"] = map[string]any{"type": "array", "items": enum(state.Candidate.AvailableEvidenceRefs()), "description": botWorkCitationGuidance}
		}
		return object(map[string]any{"candidate_digest": digest, "criteria": map[string]any{"type": "array", "items": criterion, "maxItems": 64}}, "candidate_digest", "criteria")
	default:
		return nil
	}
}
