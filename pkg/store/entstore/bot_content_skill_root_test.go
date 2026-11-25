package entstore

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotContentSkillRootPersistence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			db, sqlDB, b, _, reopen := trackedRootDB(t, scope)
			now := time.Now().UTC()
			skill := store.BotSkill{ID: uuid.New(), BotID: b, Key: "root", Title: "Root", Status: store.BotSkillDraft, Revision: 1, HeadRevision: 1, HeadDigest: strings.Repeat("a", 64), CreatedAt: now, UpdatedAt: now}
			saved := botStoredSkill{Skill: skill, Requests: map[string]botSkillReplay{"create": {Hash: strings.Repeat("b", 64), Skill: &skill}}}
			if err := db.Transaction(t.Context(), func(tx botDraftDB) error { return tx.(botSkillDB).writeSkill(t.Context(), saved, 0) }); err != nil {
				t.Fatal(err)
			}
			sentinel := "SENTINEL-replay-work-prose"
			invalid := saved
			invalid.Requests = map[string]botSkillReplay{"bad": {Hash: strings.Repeat("c", 64), Work: &store.BotWorkState{Unresolved: []string{sentinel}}}}
			if err := db.(botSkillDB).writeSkill(t.Context(), invalid, 1); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatal("Work replay accepted", err)
			}
			var raw []byte
			if err := sqlDB.QueryRow("SELECT data FROM bot_skills WHERE id = ?", skill.ID.String()).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			assertTrackedRootNoBody(t, raw, sentinel)
			if strings.Contains(string(raw), "work") {
				t.Fatal("generic replay in SQL")
			}
			db = reopen()
			got, err := db.(botSkillDB).readSkill(t.Context(), b, skill.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(saved, *got) {
				t.Fatalf("replay changed: %#v", got)
			}
			// A replay must preserve the original result after the current head changes.
			next := *got
			next.Skill.Revision++
			next.Skill.Title = "Updated"
			if err = db.(botSkillDB).writeSkill(t.Context(), next, 1); err != nil {
				t.Fatal(err)
			}
			got, err = db.(botSkillDB).readSkill(t.Context(), b, skill.ID)
			if err != nil || got.Requests["create"].Skill.Title != "Root" || got.Skill.Title != "Updated" {
				t.Fatal("replay snapshot changed", err)
			}
		})
	}
}
func TestBotContentSkillRootRejectsEscapes(t *testing.T) {
	skill := store.BotSkill{ID: uuid.New(), BotID: uuid.New(), Key: "root", Title: "Root", Status: store.BotSkillDraft, Revision: 1}
	good := botStoredSkill{Skill: skill}
	data, err := encodeSkillRoot(good)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := botJSONMap(good)
	if _, err = decodeSkillRoot(old); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatal("legacy root accepted", err)
	}
	var record map[string]any
	_ = json.Unmarshal([]byte(data["bot_record_v2"].(string)), &record)
	record["requests"] = map[string]any{"bad": map[string]any{"hash": strings.Repeat("a", 64), "work": map[string]any{"summary": "SENTINEL-escape"}}}
	raw, _ := json.Marshal(record)
	if _, err = decodeSkillRoot(map[string]any{"bot_record_v2": string(raw)}); err == nil {
		t.Fatal("SQL Work escape accepted")
	}
	for _, title := range []string{strings.Repeat("界", 86), string([]byte{0xff})} {
		s := good
		s.Skill.Title = title
		if _, err = encodeSkillRoot(s); !errors.Is(err, store.ErrBotInvalid) {
			t.Fatal("invalid title accepted", err)
		}
	}
	s := good
	foreign := skill
	foreign.BotID = uuid.New()
	s.Requests = map[string]botSkillReplay{"create": {Hash: strings.Repeat("a", 64), Skill: &foreign}}
	if _, err = encodeSkillRoot(s); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatal("foreign result accepted", err)
	}
	s.Requests = map[string]botSkillReplay{"create": {Hash: "body"}}
	if _, err = encodeSkillRoot(s); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatal("non-hash replay accepted", err)
	}
}
