package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func legacySkillID(botID, triggerID uuid.UUID, version int64) uuid.UUID {
	return uuid.NewSHA1(botID, []byte(fmt.Sprintf("legacy-skill:%s:%d", triggerID, version)))
}

// Conversion is explicit owner consent to import, not evidence that an old
// assistant-authored procedure was owner-specified or successfully executed.
// The association is stored with the skill; immutable triggers and grants are
// deliberately never rewritten, so scheduler identity and next-run survive.
func (r *botRepository) convertLegacySkill(ctx context.Context, q store.BotSkillLegacyConversion) (*store.BotSkill, error) {
	hash, err := skillRequestHash(q, q.Mutation)
	if err != nil {
		return nil, err
	}
	var out *store.BotSkill
	err = r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := r.skillMutation(ctx, tx, q.Mutation, false)
		if err != nil {
			return err
		}
		if q.TriggerID == uuid.Nil || q.Version < 1 || q.ExpectedBotRevision < 1 {
			return store.ErrBotInvalid
		}
		db := tx.(botSkillDB)
		id := legacySkillID(q.Mutation.BotID, q.TriggerID, q.Version)
		if prior, e := db.readSkill(ctx, q.Mutation.BotID, id); e == nil {
			replay, ok := prior.Requests[q.Mutation.IdempotencyKey]
			if !ok || replay.Hash != hash || replay.Skill == nil {
				return store.ErrBotConflict
			}
			copy := *replay.Skill
			out = &copy
			return nil
		} else if !errors.Is(e, store.ErrBotNotFound) {
			return e
		}
		if rec.Bot.Version != q.Version || rec.Bot.Revision != q.ExpectedBotRevision {
			return store.ErrBotConflict
		}
		if rec.ActiveRunID != nil {
			return store.ErrBotBusy
		}
		v, err := tx.ReadVersion(ctx, q.Mutation.BotID, q.Version)
		if err != nil {
			return err
		}
		var trigger *store.TriggerSpec
		for i := range v.Triggers {
			if v.Triggers[i].ID == q.TriggerID {
				trigger = &v.Triggers[i]
				break
			}
		}
		if trigger == nil {
			return store.ErrBotNotFound
		}
		if trigger.Kind != store.TriggerCron || trigger.SkillID != nil || strings.TrimSpace(trigger.Instructions) == "" {
			return store.ErrBotInvalid
		}
		if skillSHA(trigger.Instructions) != q.InstructionsDigest {
			return store.ErrBotConflict
		}
		if !legacySkillSafeText(trigger.Instructions) {
			return store.ErrBotInvalid
		}
		association := &store.BotSkillLegacyAssociation{TriggerID: q.TriggerID, Version: q.Version, InstructionsDigest: q.InstructionsDigest}
		p := store.BotSkillProvenance{Level: store.BotSkillImported, BotID: q.Mutation.BotID, Legacy: association, SourceDigest: q.InstructionsDigest, Quote: trigger.Instructions, RecordedAt: r.clock()}
		revision := store.BotSkillRevision{SchemaVersion: store.BotSkillSchemaVersion, ID: uuid.New(), BotID: q.Mutation.BotID, SkillID: id, Revision: 1, Responsibility: "Imported scheduled task", Expectations: []store.BotSkillExpectation{{ID: "legacy-task", Text: trigger.Instructions, Provenance: p}}, Method: []string{trigger.Instructions}, Provenance: []store.BotSkillProvenance{p}, ChangeReason: "Explicit owner import of legacy procedure; not verified learning or owner-authored requirements"}
		revision.Digest = revision.ContentDigest()
		if err := revision.Validate(); err != nil {
			return err
		}
		count, size, err := db.skillQuota(ctx, q.Mutation.BotID)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(revision)
		if err := store.ValidateBotSkillQuota(count, size, int64(len(raw))); err != nil {
			return err
		}
		skill := store.BotSkill{ID: id, BotID: q.Mutation.BotID, Key: "legacy-" + id.String(), Title: "Imported scheduled task", Status: store.BotSkillDraft, Revision: 1, HeadRevision: 1, HeadDigest: revision.Digest, Legacy: association, CreatedAt: r.clock(), UpdatedAt: r.clock()}
		saved := botStoredSkill{Skill: skill, Requests: map[string]botSkillReplay{q.Mutation.IdempotencyKey: {Hash: hash, Skill: &skill}}}
		if err = db.writeSkill(ctx, saved, 0); err != nil {
			return err
		}
		if err = db.insertSkillRevision(ctx, revision); err != nil {
			return err
		}
		out = &skill
		return nil
	})
	return out, err
}

func legacySkillSafeText(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{"{{credential:", "<<<", "-----begin", "authorization:", "password=", "token=", "api_key=", "?", "#"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}

func resolveImportedSkillSource(ctx context.Context, tx botDraftDB, p store.BotSkillProvenance, b uuid.UUID) error {
	if p.Legacy == nil {
		return store.ErrBotInvalid
	}
	a := p.Legacy
	skill, err := tx.(botSkillDB).readSkill(ctx, b, legacySkillID(b, a.TriggerID, a.Version))
	if err != nil {
		return err
	}
	if skill.Skill.Legacy == nil || *skill.Skill.Legacy != *a {
		return store.ErrBotForbidden
	}
	v, err := tx.ReadVersion(ctx, b, a.Version)
	if err != nil {
		return err
	}
	for _, trigger := range v.Triggers {
		if trigger.ID == a.TriggerID && skillSHA(trigger.Instructions) == a.InstructionsDigest && p.SourceDigest == a.InstructionsDigest && p.Quote == trigger.Instructions {
			return nil
		}
	}
	return store.ErrBotForbidden
}
