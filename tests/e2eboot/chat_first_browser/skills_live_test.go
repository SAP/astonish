package main

import (
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"reflect"
	"testing"
	"time"
)

func TestSkillsOwnerRevisionExactSourceAndRetention(t *testing.T) {
	_, source, before := skillsTestPin()
	after := before
	after.Revision++
	after.Digest = "different"
	p := before.Provenance[0]
	after.Expectations = append(append([]store.BotSkillExpectation{}, before.Expectations...), store.BotSkillExpectation{ID: "humidity", Text: "Relative humidity percentage", Provenance: p})
	if err := skillsOwnerRevision(before, after, source); err != nil {
		t.Fatal(err)
	}
	bad := source
	bad.ID = uuid.New()
	if skillsOwnerRevision(before, after, bad) == nil {
		t.Fatal("foreign source")
	}
	bad = source
	bad.UserEventID = "old"
	if skillsOwnerRevision(before, after, bad) == nil {
		t.Fatal("replayed owner event")
	}
	after.Expectations = after.Expectations[1:]
	if skillsOwnerRevision(before, after, source) == nil {
		t.Fatal("lost expectations")
	}
}
func TestSkillsAssignmentCannotSchedule(t *testing.T) {
	if skillsNoSchedule(employeeDisk{}) != nil {
		t.Fatal("empty")
	}
	if skillsNoSchedule(employeeDisk{Jobs: []json.RawMessage{json.RawMessage(`{}`)}}) == nil {
		t.Fatal("accepted schedule")
	}
}
func TestSkillsEndpointReaderUsesActualScopedRoutes(t *testing.T) {
	_, _, revision := skillsTestPin()
	requests := []string{}
	p := &skillsProbe{botPath: "/api/studio/bots/" + revision.BotID.String(), state: map[string]any{"bot_id": revision.BotID.String()}, skill: revision.SkillID,
		api: func(method, path string, body any) string {
			if method != "GET" || body != nil {
				t.Fatal("observer wrote state")
			}
			requests = append(requests, path)
			raw, err := json.Marshal(revision)
			if err != nil {
				t.Fatal(err)
			}
			return string(raw)
		}}
	got := p.head()
	if !reflect.DeepEqual(got, revision) {
		t.Fatal("head differs from endpoint revision")
	}
	var old store.BotSkillRevision
	p.get(fmt.Sprintf("/%s/revisions/1", revision.SkillID), &old)
	if !reflect.DeepEqual(old, revision) {
		t.Fatal("immutable revision differs from endpoint revision")
	}
	if len(requests) != 2 || requests[0] != p.botPath+"/skills/"+p.skill.String()+"/head?scope=personal" || requests[1] != p.botPath+"/skills/"+p.skill.String()+"/revisions/1?scope=personal" {
		t.Fatal(requests)
	}
}

func TestSkillsEndpointReaderRejectsInvalidOrForeignHead(t *testing.T) {
	for _, name := range []string{"digest", "owner-quote", "foreign-bot", "foreign-skill"} {
		t.Run(name, func(t *testing.T) {
			_, _, revision := skillsTestPin()
			p := &skillsProbe{botPath: "/api/studio/bots/" + revision.BotID.String(), state: map[string]any{"bot_id": revision.BotID.String()}, skill: revision.SkillID}
			switch name {
			case "digest":
				revision.Digest = "corrupt"
			case "owner-quote":
				revision.Expectations[0].Provenance.Quote = "weather"
				revision.Digest = revision.ContentDigest()
			case "foreign-bot":
				revision.BotID = uuid.New()
				revision.Provenance[0].BotID = revision.BotID
				revision.Expectations[0].Provenance.BotID = revision.BotID
				revision.Digest = revision.ContentDigest()
			case "foreign-skill":
				revision.SkillID = uuid.New()
				revision.Digest = revision.ContentDigest()
			}
			if (name == "foreign-bot" || name == "foreign-skill") && revision.Validate() != nil {
				t.Fatal("foreign revision must be valid to exercise reader identity check")
			}
			raw, err := json.Marshal(revision)
			if err != nil {
				t.Fatal(err)
			}
			p.api = func(method, path string, body any) string {
				if method != "GET" || body != nil || path != p.botPath+"/skills/"+p.skill.String()+"/head?scope=personal" {
					t.Fatal("unexpected scoped observer request", method, path)
				}
				return string(raw)
			}
			defer func() {
				if recover() == nil {
					t.Error("accepted invalid or foreign skill head")
				}
			}()
			p.head()
		})
	}
}
func TestSkillsThinScheduleRequiresExactBinding(t *testing.T) {
	_, run, revision := skillsTestPin()
	trigger := uuid.New()
	p := &skillsProbe{skill: revision.SkillID, state: map[string]any{"bot_id": run.BotID.String()}}
	version := store.BotVersion{BotID: run.BotID, Triggers: []store.TriggerSpec{{ID: trigger, SkillID: &revision.SkillID, Invocation: "Current report", Enabled: true}}}
	p.schedule(version, trigger)
	for _, name := range []string{"foreign-bot", "wrong-skill", "missing-invocation", "disabled", "wrong-trigger"} {
		t.Run(name, func(t *testing.T) {
			bad := version
			bad.Triggers = append([]store.TriggerSpec{}, version.Triggers...)
			switch name {
			case "foreign-bot":
				bad.BotID = uuid.New()
			case "wrong-skill":
				other := uuid.New()
				bad.Triggers[0].SkillID = &other
			case "missing-invocation":
				bad.Triggers[0].Invocation = ""
			case "disabled":
				bad.Triggers[0].Enabled = false
			case "wrong-trigger":
				bad.Triggers[0].ID = uuid.New()
			}
			defer func() {
				if recover() == nil {
					t.Error("accepted invalid schedule")
				}
			}()
			p.schedule(bad, trigger)
		})
	}
}

func TestSkillsAgentWorkReadsSaveOnlyRevisionAndFreshStream(t *testing.T) {
	_, run, revision := skillsTestPin()
	started := time.Now().UTC().Add(-time.Minute)
	at := started.Add(time.Second)
	run.SessionID = revision.Expectations[0].Provenance.SessionID
	run.Status, run.CreatedAt, run.StartedAt, run.FinishedAt = store.RunSucceeded, at, &at, &at
	run.Report.Summary = "Fresh weather report"
	run.Report.Skill.Kind = store.BotSkillCreated
	run.Report.Skill.UsedRevision = 0
	run.Report.Skill.CommittedRevision = revision.Revision
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: run.BotID, RunID: run.ID, Version: 1, Instructions: "Current report", Charter: "Useful work"})
	w.Phase, w.AgentWork.Disposition = store.BotWorkFinished, store.AgentWorkComplete
	w.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "answer", Text: run.Report.Summary}
	w.Candidate.Digest = w.Candidate.ContentDigest()
	run.Work = store.BuildBotWorkProjection(w)
	raw, err := json.Marshal(map[string]any{"run": run, "work": w})
	if err != nil {
		t.Fatal(err)
	}
	p := &skillsProbe{root: t.TempDir(), agent: true, startedAt: started, botPath: "/api/studio/bots/" + run.BotID.String(), state: map[string]any{"bot_id": run.BotID.String()}, runs: func() []store.BotRun { return []store.BotRun{run} }, read: func(_, _ string) employeeDisk {
		return employeeDisk{Runs: []employeeDiskRow{{ID: run.ID.String(), BotID: run.BotID.String(), Data: string(raw)}}}
	}}
	requests := []string{}
	p.api = func(method, path string, body any) string {
		if method != "GET" || body != nil {
			t.Fatal("mutating observer")
		}
		requests = append(requests, path)
		switch path {
		case p.botPath + fmt.Sprintf("/skills/%s/revisions/1?scope=personal", revision.SkillID):
			data, _ := json.Marshal(revision)
			return string(data)
		case p.botPath + "?scope=personal":
			data, _ := json.Marshal(store.Bot{ID: run.BotID, OwnerID: uuid.New(), Scope: store.BotScopePersonal})
			return string(data)
		case p.botPath + "/sessions/" + run.SessionID.String() + "/stream?scope=personal":
			return `{"messages":[{"type":"tool_call","toolName":"web_fetch","toolId":"source"},{"type":"tool_result","toolName":"web_fetch","toolId":"source","toolResult":{"status":200,"url":"https://weather.example/current","content":"Current measured weather report payload"}}]}`
		default:
			t.Fatalf("unexpected read: %s", path)
			return ""
		}
	}
	got := p.work(run)
	if got.Contract.Skill != nil || len(p.captures) != 1 || len(requests) != 3 {
		t.Fatal("save fabricated use or lacked immutable/fresh readback")
	}
}

func TestSkillsWorkRequiresActualPublicRun(t *testing.T) {
	_, run, _ := skillsTestPin()
	p := &skillsProbe{runs: func() []store.BotRun { return nil }}
	defer func() {
		if recover() == nil {
			t.Error("missing actual run accepted")
		}
	}()
	p.work(run)
}

func TestSkillsDisabledDoesNotTouchResources(t *testing.T) {
	t.Setenv("BOT_SKILLS_ACCEPTANCE", "")
	if skillsJourneyTimeout() != 30*time.Minute {
		t.Fatal("legacy timeout changed")
	}
	defer func() {
		t.Setenv("BOT_SKILLS_ACCEPTANCE", "1")
		if skillsJourneyTimeout() != 60*time.Minute {
			t.Fatal("skills timeout")
		}
	}()
	if startSkills("", "", nil, nil, nil, nil, nil) != nil {
		t.Fatal("enabled")
	}
	var p *skillsProbe
	p.restored()
	p.removeAndInspect(nil, nil, nil)
}
