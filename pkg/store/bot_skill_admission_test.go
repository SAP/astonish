package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestBotSkillRunAdmissionShapeAndCompatibility(t *testing.T) {
	revision := skillFixture()
	original := revision.ContentDigest()
	raw, err := json.Marshal(revision)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"source":`) {
		t.Fatal("absent source changed historical wire representation")
	}
	var restored BotSkillRevision
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.ContentDigest() != original {
		t.Fatal("historical digest changed")
	}
	p := revision.Expectations[0].Provenance
	p.Source, p.EventID = BotSkillSourceRunAdmission, ""
	if err := p.Validate(revision.BotID); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*BotSkillProvenance){
		"unknown":    func(p *BotSkillProvenance) { p.Source = "future" },
		"event":      func(p *BotSkillProvenance) { p.EventID = "user" },
		"attachment": func(p *BotSkillProvenance) { p.AttachmentID = "attachment" },
		"goal":       func(p *BotSkillProvenance) { id := uuid.New(); p.GoalID = &id; p.GoalRevision = 1 },
		"observed":   func(p *BotSkillProvenance) { p.Level = BotSkillObserved },
		"evidence":   func(p *BotSkillProvenance) { p.EvidenceRefs = []string{"observation:0"} },
		"quote":      func(p *BotSkillProvenance) { p.Quote = "" },
		"session":    func(p *BotSkillProvenance) { p.SessionID = uuid.Nil },
		"digest":     func(p *BotSkillProvenance) { p.SourceDigest = "bad" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := p
			mutate(&bad)
			if bad.Validate(revision.BotID) == nil {
				t.Fatal("ambiguous admission accepted")
			}
		})
	}
}
