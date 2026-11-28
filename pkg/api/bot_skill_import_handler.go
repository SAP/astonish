package api

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"net/http"
)

// BotLegacySkillImportHandler is an explicit, owner-authorized conversion. No
// GET/startup path imports procedures and no caller-supplied skill content is used.
func BotLegacySkillImportHandler(w http.ResponseWriter, r *http.Request) {
	repo, id, ok := botRequestSkills(w, r)
	if !ok {
		return
	}
	var q struct {
		ExpectedRevision   int64     `json:"expected_revision"`
		TriggerID          uuid.UUID `json:"trigger_id"`
		Version            int64     `json:"version"`
		InstructionsDigest string    `json:"instructions_digest"`
		IdempotencyKey     string    `json:"idempotency_key"`
	}
	if !botDecodeRequest(w, r, &q) {
		return
	}
	result, err := repo.ConvertLegacySkill(r.Context(), store.BotSkillLegacyConversion{Mutation: store.BotSkillMutation{BotID: id, IdempotencyKey: q.IdempotencyKey}, ExpectedBotRevision: q.ExpectedRevision, TriggerID: q.TriggerID, Version: q.Version, InstructionsDigest: q.InstructionsDigest})
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusOK, result)
}
