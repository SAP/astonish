package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// waitBotLearningEligibilityRun only observes the admitted identity. It never
// polls the worker or retries admission: a commit failure must remain a failure,
// not turn into a successful second execution. Non-running outcomes are returned
// immediately for the caller's strict success/partial assertion.
func waitBotLearningEligibilityRun(t *testing.T, f *interactiveHTTPFixture, run *store.BotRun) *store.BotRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for run.Status == store.RunQueued || run.Status == store.RunRunning {
		select {
		case <-ctx.Done():
			t.Fatalf("learning run %s did not finish: %v; status=%s lease=%+v report=%+v work=%+v", run.ID, ctx.Err(), run.Status, run.Lease, run.Report, run.Work)
		case <-ticker.C:
			next, err := f.repo.GetRun(ctx, f.b.ID, run.ID)
			if err != nil {
				t.Fatalf("read learning run %s: %v; last status=%s report=%+v work=%+v", run.ID, err, run.Status, run.Report, run.Work)
			}
			run = next
		}
	}
	return run
}

// Exercise production HTTP admission, worker, skill staging and terminal commit.
func TestBotAgentLearningEligibilityHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, mode := range []capabilityHTTPMode{capabilityInteractive, capabilityAutonomous} {
			for _, scenario := range []string{"partial-method", "unsupported-method", "rejected-method", "supported-method", "preselection-only", "postselection-use"} {
				t.Run(string(scope)+"/"+string(mode)+"/"+scenario, func(t *testing.T) {
					f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
					defer f.worker.Stop()
					step := 0
					f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
						step++
						part := &genai.Part{Text: "The receipt was read; the proposed method is a draft."}
						if step == 1 {
							part = capabilityHTTPCall(mode, "seed-read", "read_file", map[string]any{"path": "/isolated/receipt.txt"})
						}
						if step == 2 {
							part = capabilityHTTPCall(mode, "seed-save", "bot_skill_save", map[string]any{"expected_revision": f.model.work().Revision, "title": "Read receipt", "method": []string{"Read the current receipt."}, "evidence_refs": []string{"capture:seed-read"}})
						}
						return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}, true
					}
					seed := waitBotLearningEligibilityRun(t, f, f.capabilityTurn(mode, []string{"read_file", "web_fetch", "bot_skill_save", "bot_skill_use", "bot_work_update"}))
					skills := f.repo.(store.BotSkillStore)
					list, err := skills.ListSkills(f.ctx, f.b.ID, store.BotSkillPage{})
					if err != nil || len(list.Skills) != 1 || seed.Status != store.RunSucceeded {
						t.Fatalf("seed: %+v %+v %v", seed, list, err)
					}
					if seed.FinishedAt == nil || seed.Report.Work == nil || seed.Report.Skill == nil || step != 3 || f.pool.calls != 1 {
						t.Fatalf("seed lacks final commit or duplicated work: run=%+v report=%+v steps=%d reads=%d", seed, seed.Report, step, f.pool.calls)
					}
					selected := list.Skills[0]
					// Reconciliation of a committed seed must not run the model or
					// source again, create another revision, or duplicate publication.
					if err := f.worker.Poll(f.ctx); err != nil {
						t.Fatal(err)
					}
					persisted, err := f.repo.GetRun(f.ctx, f.b.ID, seed.ID)
					if err != nil || persisted.Status != store.RunSucceeded || persisted.FinishedAt == nil || step != 3 || f.pool.calls != 1 {
						t.Fatalf("seed reconciliation replayed work: run=%+v err=%v steps=%d reads=%d", persisted, err, step, f.pool.calls)
					}
					headAfterPoll, err := skills.GetSkill(f.ctx, f.b.ID, selected.ID)
					if err != nil || headAfterPoll.HeadRevision != selected.HeadRevision {
						t.Fatalf("seed reconciliation changed skill: %+v %v", headAfterPoll, err)
					}
					history := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200))
					if strings.Count(history, "bot-report:"+seed.ID.String()) != 1 {
						t.Fatal("seed final publication missing or duplicated")
					}
					if selected.Status != store.BotSkillDraft {
						t.Fatal("newly learned method retroactively activated from pre-creation work")
					}
					old, err := skills.GetSkillRevision(f.ctx, f.b.ID, selected.ID, selected.HeadRevision)
					if err != nil {
						t.Fatal(err)
					}
					f.pool.callOverride = func(name string, args map[string]interface{}) (json.RawMessage, error) {
						if name != "web_fetch" {
							return nil, fmt.Errorf("unexpected %s", name)
						}
						return json.Marshal(map[string]any{"final_url": "https://example.test/receipt", "content": "current receipt", "status": 200})
					}
					step = 0
					f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
						step++
						part := &genai.Part{Text: "Here is the receipt result; remaining limitations are explicit."}
						switch step {
						case 1:
							if scenario == "preselection-only" {
								part = capabilityHTTPCall(mode, "source-read", "web_fetch", map[string]any{"url": "https://example.test/receipt", "method": "GET"})
							} else {
								part = capabilityHTTPCall(mode, "use", "bot_skill_use", map[string]any{"expected_revision": f.model.work().Revision, "skill_id": selected.ID.String()})
							}
						case 2:
							if scenario == "preselection-only" {
								part = capabilityHTTPCall(mode, "use", "bot_skill_use", map[string]any{"expected_revision": f.model.work().Revision, "skill_id": selected.ID.String()})
							} else {
								part = capabilityHTTPCall(mode, "source-read", "web_fetch", map[string]any{"url": "https://example.test/receipt", "method": "GET"})
							}
						case 3:
							if scenario == "partial-method" || scenario == "unsupported-method" || scenario == "supported-method" || scenario == "rejected-method" {
								refs := []string{}
								if scenario != "unsupported-method" {
									refs = []string{"capture:source-read"}
								}
								method := []string{"An unrelated replacement procedure."}
								if scenario == "rejected-method" {
									method = []string{""}
								}
								part = capabilityHTTPCall(mode, "change-method", "bot_skill_save", map[string]any{"expected_revision": f.model.work().Revision, "method": method, "evidence_refs": refs})
							}
						case 4:
							if scenario == "partial-method" {
								part = capabilityHTTPCall(mode, "partial", "bot_work_update", map[string]any{"expected_revision": f.model.work().Revision, "kind": "set_disposition", "disposition": "partial"})
							}
						}
						return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}, true
					}
					run := waitBotLearningEligibilityRun(t, f, botAgentLearningTurn(t, f, mode, "eligibility"))
					wantStatus := store.RunSucceeded
					if scenario == "partial-method" {
						wantStatus = store.RunPartial
					}
					if run.Status != wantStatus {
						t.Fatalf("status %+v", run)
					}
					head, err := skills.GetSkill(f.ctx, f.b.ID, selected.ID)
					if err != nil {
						t.Fatal(err)
					}
					rev, err := skills.GetSkillRevision(f.ctx, f.b.ID, selected.ID, head.HeadRevision)
					if err != nil {
						t.Fatal(err)
					}
					if len(rev.Sources) != 1 || rev.Sources[0].Observation == nil || rev.Sources[0].Observation.HTTPStatus != 200 {
						t.Fatalf("source facts discarded with method: %+v receipt=%+v", rev, run.Report.Skill)
					}
					wantMethod := strings.Join(old.Method, "\n")
					if scenario == "supported-method" {
						wantMethod = "An unrelated replacement procedure."
					}
					if strings.Join(rev.Method, "\n") != wantMethod {
						t.Fatalf("ineligible method committed: %+v", rev)
					}
					if scenario == "preselection-only" && head.Status != store.BotSkillDraft {
						t.Fatal("preselection evidence retroactively activated skill")
					}
					if scenario == "postselection-use" && head.Status != store.BotSkillActive {
						t.Fatal("subsequent execution did not activate skill")
					}
				})
			}
		}
	}
}
