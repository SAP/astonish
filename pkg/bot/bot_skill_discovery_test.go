package bot

import (
	"encoding/json"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestBotSkillDiscoveryReadJSONOptional(t *testing.T) {
	raw, err := json.Marshal(BotWorkReadResult{})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"skill_index", "skill_next", "skill_index_unavailable", "prior_skill_id"} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("new optional field emitted in old read: %s", raw)
		}
	}
	id := uuid.New()
	want := BotWorkReadResult{SkillIndex: []BotWorkSkillHead{{ID: id, Title: "method", HeadRevision: 1}}, SkillNext: "opaque-cursor", PriorSkillID: &id}
	raw, err = json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got BotWorkReadResult
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.SkillIndex) != 1 || got.SkillIndex[0].ID != id || got.SkillNext != want.SkillNext || got.PriorSkillID == nil || *got.PriorSkillID != id {
		t.Fatalf("discovery lost in read JSON: %+v", got)
	}
}
