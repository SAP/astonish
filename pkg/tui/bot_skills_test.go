package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type botSkillsTestBackend struct {
	testBotBackend
	fail bool
}

func (b *botSkillsTestBackend) BotSkills(context.Context, store.BotSkillPage) (*store.BotSkillList, error) {
	if b.fail {
		return nil, errors.New("offline")
	}
	return &store.BotSkillList{Skills: []store.BotSkill{{ID: uuid.New(), Title: "Research", Status: store.BotSkillActive, HeadRevision: 2}}}, nil
}
func (b *botSkillsTestBackend) BotSkillRevisions(context.Context, uuid.UUID, store.BotSkillPage) (*store.BotSkillRevisionList, error) {
	return &store.BotSkillRevisionList{Revisions: []store.BotSkillRevision{{SchemaVersion: 1, Revision: 2, Responsibility: "Verify sources", ChangeReason: "Owner correction"}}}, nil
}
func (b *botSkillsTestBackend) BotSkillRuns(context.Context, uuid.UUID, store.BotSkillPage) (*store.BotSkillRunList, error) {
	return &store.BotSkillRunList{Runs: []store.BotRun{{Report: store.BotReport{Skill: &store.BotSkillReceipt{SchemaVersion: 1, Kind: store.BotSkillUnchanged, UsedRevision: 2}}}}}, nil
}
func TestBotSkillsInspectAndEscapePreservesWork(t *testing.T) {
	b := &botSkillsTestBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New()}}}
	m := newModel(context.Background(), Config{Backend: b})
	m.height = 40
	m.ta.SetValue("unfinished")
	next, cmd := m.botCommand("/bots skills")
	m = next.(model)
	if !strings.Contains(m.renderBotsOverlay(), "Loading") {
		t.Fatal("no loading")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	if !strings.Contains(m.renderBotsOverlay(), "Research") {
		t.Fatal("missing skill")
	}
	next, cmd = m.handleBotSkillsKey("enter")
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	if !strings.Contains(m.renderBotsOverlay(), "Verify sources") {
		t.Fatal("missing committed revision")
	}
	next, cmd = m.handleBotSkillsKey("u")
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	if !strings.Contains(m.renderBotsOverlay(), "unchanged") {
		t.Fatal("missing receipt")
	}
	next, cmd = m.handleBotSkillsKey("esc")
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	next, _ = m.handleBotSkillsKey("esc")
	m = next.(model)
	if m.bots.open || m.ta.Value() != "unfinished" || b.selected == nil {
		t.Fatal("inspection lost Bot or input")
	}
}
func TestBotSkillsStaleGenerationAndErrors(t *testing.T) {
	b := &botSkillsTestBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New()}}}
	m := newModel(context.Background(), Config{Backend: b})
	next, old := m.openBotSkills()
	m = next.(model)
	next, _ = m.handleBotSkillsKey("esc")
	m = next.(model)
	next, _ = m.Update(old())
	m = next.(model)
	if m.bots.open {
		t.Fatal("late read reopened overlay")
	}
	next, cmd := m.openBotSkills()
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	b.fail = true
	next, cmd = m.handleBotSkillsKey("r")
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	if !strings.Contains(m.renderBotsOverlay(), "stale") {
		t.Fatal("error not visible")
	}
	next, cmd = m.handleBotSkillsKey("enter")
	if cmd != nil || next.(model).bots.skills.mode != botSkillsList {
		t.Fatal("stale selection accepted")
	}
}
func TestBotSkillsErrorIsNotEmpty(t *testing.T) {
	b := &botSkillsTestBackend{fail: true, testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New()}}}
	m := newModel(context.Background(), Config{Backend: b})
	m.height = 40
	next, cmd := m.openBotSkills()
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	text := m.renderBotSkills()
	if !strings.Contains(text, "Unavailable") || strings.Contains(text, "No committed skills") {
		t.Fatal(text)
	}
}
func TestBotSkillsRevertRevisionPointer(t *testing.T) {
	for _, n := range []int64{0, 42} {
		text := skillRevisionText(store.BotSkillRevision{SchemaVersion: store.BotSkillSchemaVersion, RevertsRevision: &n})
		if !strings.Contains(text, fmt.Sprintf("Reverts revision %d", n)) {
			t.Fatal(text)
		}
	}
	if text := skillRevisionText(store.BotSkillRevision{SchemaVersion: store.BotSkillSchemaVersion}); strings.Contains(text, "Reverts revision") {
		t.Fatal(text)
	}
}

func TestBotSkillsAccessObservationIsNotUsefulness(t *testing.T) {
	text := skillRevisionText(store.BotSkillRevision{SchemaVersion: store.BotSkillSchemaVersion, Sources: []store.BotSkillSource{{URL: "https://example.test", Purpose: "Compare official data", Observation: &store.BotSkillSourceObservation{HTTPStatus: 200, CallID: "call-1"}}}})
	for _, want := range []string{"Access observation:", "HTTP 200", "call-1", "Source usefulness is agent judgment"} {
		if !strings.Contains(text, want) {
			t.Fatal(text)
		}
	}
}

func TestBotSkillsUnknownSchema(t *testing.T) {
	text := skillRevisionText(store.BotSkillRevision{SchemaVersion: 99, Responsibility: "untrusted"})
	if !strings.Contains(text, "Unsupported") || strings.Contains(text, "untrusted") {
		t.Fatal(text)
	}
}
