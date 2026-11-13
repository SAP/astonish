package api

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"sort"
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
	case botWorkBlockedReviewPrompt:
		schema := botWorkResponseSchema(botWorkReviewPrompt, inputs...)
		assessment := object(map[string]any{
			"dependency_purpose": enum([]string{string(store.BotWorkDependencyConfigureDelivery), string(store.BotWorkDependencySend), string(store.BotWorkDependencySavePreference), string(store.BotWorkDependencyUnrelated)}),
			"dependency_verdict": enum([]string{string(store.BotCriterionSatisfied), string(store.BotCriterionFailed), string(store.BotCriterionUnverified)}),
			"blocker_id":         enum([]string{"owner-email-transport"}), "criterion_ids": map[string]any{"type": "array", "items": enum(ids), "maxItems": 64},
			"source_id": enum([]string{"assignment"}), "source_quote": text(),
			"verdict": enum([]string{string(store.BotCriterionSatisfied), string(store.BotCriterionFailed), string(store.BotCriterionUnverified)}),
			"method":  enum([]string{string(store.BotAssessmentModel)}), "explanation": text(),
		}, "dependency_purpose", "dependency_verdict", "blocker_id", "criterion_ids", "source_id", "source_quote", "verdict", "method", "explanation")
		schema["properties"].(map[string]any)["blocked_response"] = map[string]any{"anyOf": []any{assessment, map[string]any{"type": "null"}}}
		schema["required"] = append(schema["required"].([]string), "blocked_response")
		return schema
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
		result := object(map[string]any{"candidate_digest": digest, "criteria": map[string]any{"type": "array", "items": criterion, "maxItems": 64}}, "candidate_digest", "criteria")
		var content struct {
			Items             []botWorkContentItem `json:"content_items"`
			Sources           map[string]string    `json:"content_sources"`
			ExecutionEvidence map[string]string    `json:"execution_evidence"`
		}
		if len(inputs) > 0 {
			_ = json.Unmarshal(inputs[0], &content)
		}
		if content.Items != nil {
			itemIDs, sourceIDs := []string{}, []string{}
			for _, item := range content.Items {
				itemIDs = append(itemIDs, item.ID)
			}
			for ref := range content.Sources {
				sourceIDs = append(sourceIDs, ref)
			}
			sort.Strings(sourceIDs)
			verdict := func() map[string]any {
				return enum([]string{string(store.BotCriterionSatisfied), string(store.BotCriterionFailed), string(store.BotCriterionUnverified)})
			}
			citations := func(refs []string) map[string]any {
				ref := text()
				if len(refs) > 0 {
					ref = enum(refs)
				}
				array := map[string]any{"type": "array", "items": object(map[string]any{"ref": ref, "quote": text()}, "ref", "quote")}
				if len(refs) == 0 {
					array["maxItems"] = 0
				}
				return array
			}
			executionIDs := []string{}
			for ref := range content.ExecutionEvidence {
				executionIDs = append(executionIDs, ref)
			}
			sort.Strings(executionIDs)
			itemRef := text()
			if len(itemIDs) > 0 {
				itemRef = enum(itemIDs)
			}
			assessment := object(map[string]any{"item_id": itemRef, "claim_type": enum([]string{string(botContentFactual), string(botContentNonfactual), string(botContentExecution)}), "relevance": verdict(), "support": verdict(), "explanation": text(), "sources": citations(sourceIDs), "execution_sources": citations(executionIDs)}, "item_id", "claim_type", "relevance", "support", "explanation", "sources", "execution_sources")
			result["properties"].(map[string]any)["content_assessments"] = map[string]any{"type": "array", "items": assessment, "maxItems": len(itemIDs)}
			result["required"] = []string{"candidate_digest", "criteria", "content_assessments"}
		}
		return result
	default:
		return nil
	}
}
