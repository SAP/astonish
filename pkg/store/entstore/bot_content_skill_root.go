package entstore

import (
	"encoding/hex"
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// This root is a metadata index. It must never grow a generic replay payload.
// Work replays belong to the run's disk-backed content, not a skill root.
type botSkillRootRecord struct {
	Format   int                           `json:"format"`
	Skill    botSkillRootMetadata          `json:"skill"`
	Requests map[string]botSkillRootReplay `json:"requests,omitempty"`
}
type botSkillRootReplay struct {
	Hash  string                `json:"hash"`
	Skill *botSkillRootMetadata `json:"skill,omitempty"`
}
type botSkillRootMetadata struct {
	ID           uuid.UUID            `json:"id"`
	BotID        store.BotID          `json:"bot_id"`
	Key          string               `json:"key"`
	Title        string               `json:"title"`
	GoalID       *uuid.UUID           `json:"goal_id,omitempty"`
	Status       store.BotSkillStatus `json:"status"`
	Revision     int64                `json:"revision"`
	HeadRevision int64                `json:"head_revision"`
	HeadDigest   string               `json:"head_digest"`
	Legacy       *botSkillRootLegacy  `json:"legacy,omitempty"`
	CreatedAt    time.Time            `json:"created_at"`
	UpdatedAt    time.Time            `json:"updated_at"`
}
type botSkillRootLegacy struct {
	TriggerID          uuid.UUID `json:"trigger_id"`
	Version            int64     `json:"version"`
	InstructionsDigest string    `json:"instructions_digest"`
	GrantDigest        string    `json:"grant_digest,omitempty"`
}

func skillRootDigest(s string) bool { b, e := hex.DecodeString(s); return e == nil && len(b) == 32 }
func skillRootMetadata(s store.BotSkill) botSkillRootMetadata {
	r := botSkillRootMetadata{ID: s.ID, BotID: s.BotID, Key: s.Key, Title: s.Title, GoalID: s.GoalID, Status: s.Status, Revision: s.Revision, HeadRevision: s.HeadRevision, HeadDigest: s.HeadDigest, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
	if s.Legacy != nil {
		r.Legacy = &botSkillRootLegacy{TriggerID: s.Legacy.TriggerID, Version: s.Legacy.Version, InstructionsDigest: s.Legacy.InstructionsDigest, GrantDigest: s.Legacy.GrantDigest}
	}
	return r
}
func (r botSkillRootMetadata) hydrate() store.BotSkill {
	s := store.BotSkill{ID: r.ID, BotID: r.BotID, Key: r.Key, Title: r.Title, GoalID: r.GoalID, Status: r.Status, Revision: r.Revision, HeadRevision: r.HeadRevision, HeadDigest: r.HeadDigest, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	if r.Legacy != nil {
		s.Legacy = &store.BotSkillLegacyAssociation{TriggerID: r.Legacy.TriggerID, Version: r.Legacy.Version, InstructionsDigest: r.Legacy.InstructionsDigest, GrantDigest: r.Legacy.GrantDigest}
	}
	return s
}
func (r botSkillRootMetadata) valid() bool {
	if r.ID == uuid.Nil || r.BotID == uuid.Nil || r.Revision < 1 || len(r.Title) > 256 || !utf8.ValidString(r.Title) || r.Key == "" || len(r.Key) > 256 || !utf8.ValidString(r.Key) || r.HeadRevision < 0 || (r.HeadDigest != "" && !skillRootDigest(r.HeadDigest)) {
		return false
	}
	if r.Legacy != nil && (r.Legacy.TriggerID == uuid.Nil || r.Legacy.Version < 1 || !skillRootDigest(r.Legacy.InstructionsDigest) || (r.Legacy.GrantDigest != "" && !skillRootDigest(r.Legacy.GrantDigest))) {
		return false
	}
	switch r.Status {
	case store.BotSkillDraft, store.BotSkillActive, store.BotSkillArchived:
		return true
	}
	return false
}
func encodeSkillRoot(s botStoredSkill) (map[string]any, error) {
	r := botSkillRootRecord{Format: botDiskRecordFormat, Skill: skillRootMetadata(s.Skill)}
	if !r.Skill.valid() {
		return nil, store.ErrBotInvalid
	}
	if s.Requests != nil {
		r.Requests = make(map[string]botSkillRootReplay, len(s.Requests))
	}
	for key, replay := range s.Requests {
		if replay.Work != nil || key == "" || len(key) > 256 || !utf8.ValidString(key) || !skillRootDigest(replay.Hash) {
			return nil, store.ErrBotInvalid
		}
		out := botSkillRootReplay{Hash: replay.Hash}
		if replay.Skill != nil {
			m := skillRootMetadata(*replay.Skill)
			if !m.valid() || m.ID != r.Skill.ID || m.BotID != r.Skill.BotID {
				return nil, store.ErrBotInvalid
			}
			out.Skill = &m
		}
		r.Requests[key] = out
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return map[string]any{"bot_record_v2": string(raw)}, nil
}
func decodeSkillRoot(data map[string]any) (*botStoredSkill, error) {
	var r botSkillRootRecord
	if err := decodeBotRecord(data, &r); err != nil {
		return nil, err
	}
	if r.Format != botDiskRecordFormat {
		return nil, store.ErrBotInvalid
	}
	s := &botStoredSkill{Skill: r.Skill.hydrate()}
	if r.Requests != nil {
		s.Requests = make(map[string]botSkillReplay, len(r.Requests))
	}
	for key, replay := range r.Requests {
		out := botSkillReplay{Hash: replay.Hash}
		if replay.Skill != nil {
			m := replay.Skill.hydrate()
			out.Skill = &m
		}
		s.Requests[key] = out
	}
	if _, err := encodeSkillRoot(*s); err != nil {
		return nil, err
	}
	return s, nil
}
