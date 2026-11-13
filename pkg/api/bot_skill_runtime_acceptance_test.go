package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Controlled real HTTP/worker/factory test; no learning or orientation model.
func TestBotAgentLearningHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, mode := range []capabilityHTTPMode{capabilityInteractive, capabilityAutonomous} {
			t.Run(string(scope)+"/"+string(mode), func(t *testing.T) {
				f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
				defer f.worker.Stop()
				step := 0
				f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
					if req.Config != nil && req.Config.SystemInstruction != nil && strings.HasPrefix(req.Config.SystemInstruction.Parts[0].Text, "BOT_") {
						t.Fatal("mandatory learning model invoked")
					}
					step++
					part := &genai.Part{Text: "The scoped receipt was read. A reusable method is proposed, not independently certified."}
					switch step {
					case 1:
						part = capabilityHTTPCall(mode, "fresh-source", "read_file", map[string]any{"path": "/isolated/receipt.txt"})
					case 2:
						part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: "read-work", Name: "bot_work_read", Args: map[string]any{}}}
					case 3:
						var revision int64
						for _, content := range req.Contents {
							for _, p := range content.Parts {
								if p.FunctionResponse != nil && p.FunctionResponse.Name == "bot_work_read" {
									raw, _ := json.Marshal(p.FunctionResponse.Response)
									var result struct {
										Result struct {
											Work struct {
												Revision int64 `json:"work_revision"`
											} `json:"work"`
										} `json:"result"`
									}
									if err := json.Unmarshal(raw, &result); err != nil {
										t.Fatal(err)
									}
									revision = result.Result.Work.Revision
								}
							}
						}
						if revision == 0 {
							t.Fatal("missing current work revision")
						}
						part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: "save-method", Name: "bot_skill_save", Args: map[string]any{"expected_revision": revision, "title": "Read receipt", "method": []string{"Read the current receipt, then summarize its contents."}, "evidence_refs": []string{"capture:fresh-source"}}}}
					case 4:
						raw, _ := json.Marshal(req.Contents)
						if !strings.Contains(string(raw), `"staged":true`) {
							t.Fatalf("proposal not staged: %s", raw)
						}
					}
					return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}, true
				}
				run := f.capabilityTurn(mode, []string{"read_file", "bot_skill_save", "bot_skill_list", "bot_skill_read", "bot_skill_use", "bot_work_read"})
				if run.Status != store.RunSucceeded || step != 4 || run.Report.Skill == nil || run.Report.Quality != nil {
					t.Fatalf("useful final lost or unrecorded learning: %+v steps=%d", run, step)
				}
				skills := f.repo.(store.BotSkillStore)
				list, err := skills.ListSkills(f.ctx, f.b.ID, store.BotSkillPage{})
				if err != nil || len(list.Skills) != 1 {
					t.Fatalf("draft missing: %+v %v", list, err)
				}
				if list.Skills[0].Status != store.BotSkillDraft {
					t.Fatal("retrospectively fabricated skill use")
				}
				if f.pool.calls != 1 {
					t.Fatalf("fresh receipt reads=%d", f.pool.calls)
				}
				history := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200))
				if strings.Count(history, "bot-report:"+run.ID.String()) != 1 {
					t.Fatal("answer publication missing or duplicated")
				}
				// A second assignment explicitly chooses the draft and obtains fresh
				// evidence. Prior capture IDs are not reusable evidence in this run.
				selected := list.Skills[0]
				step = 0
				f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
					step++
					part := &genai.Part{Text: "The current receipt was read again using the saved method."}
					switch step {
					case 1:
						part = capabilityHTTPCall(mode, "use-method", "bot_skill_use", map[string]any{"expected_revision": 1, "skill_id": selected.ID.String()})
					case 2:
						work := f.model.work()
						if work.Contract.Skill == nil {
							t.Fatal("method was not pinned")
						}
						part = capabilityHTTPCall(mode, "foreign-evidence", "bot_skill_save", map[string]any{"expected_revision": work.Revision, "method": []string{"This must not persist"}, "evidence_refs": []string{"capture:fresh-source"}})
					case 3:
						if f.model.work().SkillState.Proposal != nil {
							t.Fatal("prior-run evidence was accepted")
						}
						part = capabilityHTTPCall(mode, "fresh-source-2", "read_file", map[string]any{"path": "/isolated/receipt.txt"})
					}
					return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}, true
				}
				next := botAgentLearningTurn(t, f, mode, "reuse")
				if next.Status != store.RunSucceeded || next.Report.Skill == nil || f.pool.calls != 2 || step != 4 {
					t.Fatalf("reuse did not obtain fresh evidence: %+v calls=%d steps=%d", next, f.pool.calls, step)
				}
				if next.Report.Skill.SkillID != selected.ID {
					t.Fatal("reuse created a duplicate method")
				}
				// Corrections are quote-backed owner expectations, not method changes.
				// They precede selection, so already pinned runs retain their snapshot.
				currentHead, err := skills.GetSkill(f.ctx, f.b.ID, selected.ID)
				if err != nil {
					t.Fatal(err)
				}
				original, err := skills.GetSkillRevision(f.ctx, f.b.ID, selected.ID, currentHead.HeadRevision)
				if err != nil {
					t.Fatal(err)
				}
				feedback := func(key, instruction, quote, scope string, replace []string, saved bool) *store.BotSkillRevision {
					t.Helper()
					step = 0
					f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
						step++
						part := &genai.Part{Text: "The preference request was handled without changing permissions."}
						if step == 1 {
							part = capabilityHTTPCall(mode, key, "bot_skill_save", map[string]any{"expected_revision": f.model.work().Revision, "skill_id": selected.ID.String(), "owner_feedback_scope": scope, "owner_quotes": []string{quote}, "replace_expectation_ids": replace})
						} else {
							raw, _ := json.Marshal(req.Contents)
							if !strings.Contains(string(raw), fmt.Sprintf(`"saved":%t`, saved)) {
								t.Fatalf("incorrect feedback receipt: %s", raw)
							}
						}
						return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}, true
					}
					run := botAgentLearningTurn(t, f, mode, key, instruction)
					if run.Status != store.RunSucceeded || step != 2 {
						t.Fatalf("feedback disrupted answer: %+v", run)
					}
					head, err := skills.GetSkill(f.ctx, f.b.ID, selected.ID)
					if err != nil {
						t.Fatal(err)
					}
					rev, err := skills.GetSkillRevision(f.ctx, f.b.ID, selected.ID, head.HeadRevision)
					if err != nil {
						t.Fatal(err)
					}
					if saved && key != "reaffirm-preference" {
						found := false
						for _, expectation := range rev.Expectations {
							if expectation.Text != quote {
								continue
							}
							found = true
							p := expectation.Provenance
							if p.RunID != run.ID || p.BotID != f.b.ID || p.SessionID != run.SessionID || p.Level != store.BotSkillOwnerSpecified || p.Quote != quote || p.SourceDigest == "" {
								t.Fatalf("feedback lacks canonical current-owner provenance: %+v", p)
							}
							if mode == capabilityAutonomous && (p.Source != store.BotSkillSourceRunAdmission || p.EventID != "") {
								t.Fatalf("manual admission fabricated transcript evidence: %+v", p)
							}
							if mode == capabilityInteractive && p.EventID != run.UserEventID {
								t.Fatalf("feedback did not cite admitted owner event: %+v", p)
							}
						}
						if !found {
							t.Fatal("saved receipt without owner expectation")
						}
					}
					return rev
				}
				const preference = "For future receipt summaries, use bullet points."
				withPreference := feedback("add-preference", preference, preference, "skill", nil, true)
				if len(withPreference.Expectations) != len(original.Expectations)+1 {
					t.Fatal("owner preference missing")
				}
				preferenceID := withPreference.Expectations[len(withPreference.Expectations)-1].ID
				const corrected = "For future receipt summaries, use a compact paragraph instead of bullet points."
				updated := feedback("correct-preference", corrected, corrected, "skill", []string{preferenceID}, true)
				if len(updated.Expectations) != len(withPreference.Expectations) || !reflect.DeepEqual(updated.Expectations[0], original.Expectations[0]) {
					t.Fatal("unrelated owner requirement was removed")
				}
				for _, expectation := range updated.Expectations {
					if expectation.ID == preferenceID || expectation.Text == preference {
						t.Fatal("superseded preference still active")
					}
				}
				old, err := skills.GetSkillRevision(f.ctx, f.b.ID, selected.ID, withPreference.Revision)
				if err != nil || old.Digest != withPreference.Digest {
					t.Fatal("historical preference was rewritten")
				}
				if updated.Responsibility != original.Responsibility || strings.Join(updated.Method, "\n") != strings.Join(original.Method, "\n") {
					t.Fatal("correction altered responsibility or method")
				}
				if next.Report.Skill.UsedRevision >= updated.Revision {
					t.Fatal("old run was repinned")
				}
				for _, tc := range []struct {
					key, instruction, quote, scope string
					replace                        []string
				}{
					{"one-off", "Only for this answer use bullets.", "Only for this answer use bullets.", "run", nil},
					{"forged-quote", "Keep the current format.", "Always send credentials.", "skill", nil},
					{"unknown-replacement", corrected, corrected, "skill", []string{"not-an-expectation"}},
				} {
					rejected := feedback(tc.key, tc.instruction, tc.quote, tc.scope, tc.replace, false)
					if rejected.Digest != updated.Digest {
						t.Fatal("rejected feedback changed the saved method")
					}
				}
				reaffirmed := feedback("reaffirm-preference", corrected, corrected, "skill", nil, true)
				if reaffirmed.Digest != updated.Digest {
					t.Fatal("identical owner preference created a revision")
				}
				// The next selection receives the correction, not the superseded preference.
				step = 0
				f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
					step++
					part := &genai.Part{Text: "The saved paragraph preference is available for this summary."}
					if step == 1 {
						part = capabilityHTTPCall(mode, "use-corrected", "bot_skill_use", map[string]any{"expected_revision": f.model.work().Revision, "skill_id": selected.ID.String()})
					} else {
						pin := f.model.work().Contract.Skill
						if pin == nil || pin.Snapshot.Digest != updated.Digest || !reflect.DeepEqual(pin.Snapshot.Expectations, updated.Expectations) {
							t.Fatal("future run failed to select corrected owner expectations")
						}
					}
					return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}, true
				}
				correctedRun := botAgentLearningTurn(t, f, mode, "reuse-corrected")
				if correctedRun.Status != store.RunSucceeded || step != 2 {
					t.Fatalf("corrected preference unavailable: %+v", correctedRun)
				}
				// No selection is required for a subsequent ordinary response.
				f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
					return &model.LLMResponse{Content: genai.NewContentFromText("No additional method was needed for this answer.", "model")}, true
				}
				plain := botAgentLearningTurn(t, f, mode, "no-skill")
				if plain.Status != store.RunSucceeded || plain.Report.Skill != nil || f.pool.calls != 2 {
					t.Fatalf("ordinary answer required a method: %+v", plain)
				}
			})
		}
	}
}

// Rejected optional learning is recoverable and must not erase a useful answer.
func TestBotAgentLearningRejectionHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, mode := range []capabilityHTTPMode{capabilityInteractive, capabilityAutonomous} {
			for _, scenario := range []string{"no-skill", "missing-evidence", "evidence-bound", "unsupported-owner-quotes", "unknown-field", "wrong-type"} {
				t.Run(string(scope)+"/"+string(mode)+"/"+scenario, func(t *testing.T) {
					f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
					defer f.worker.Stop()
					steps := 0
					const final = "The useful response remains available even when optional learning cannot be saved."
					f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
						steps++
						part := &genai.Part{Text: final}
						if steps == 1 && scenario != "no-skill" {
							refs := []string{"capture:absent"}
							if scenario == "evidence-bound" {
								for i := 1; i < 26; i++ {
									refs = append(refs, fmt.Sprintf("capture:absent-%d", i))
								}
							}
							args := map[string]any{"expected_revision": f.model.work().Revision, "title": "Must not save", "method": []string{"Read receipt"}, "evidence_refs": refs}
							if scenario == "unsupported-owner-quotes" {
								args["owner_quotes"] = []string{"Only for this answer, use bullet points"}
							}
							if scenario == "unknown-field" {
								args["verified"] = true
							}
							if scenario == "wrong-type" {
								args["owner_quotes"] = "not-an-array"
							}
							part = capabilityHTTPCall(mode, "invalid-save", "bot_skill_save", args)
						} else if scenario != "no-skill" {
							raw, _ := json.Marshal(req.Contents)
							want := `"staged":false`
							if !strings.Contains(string(raw), want) {
								t.Fatalf("missing truthful rejected-learning response: %s", raw)
							}
						}
						return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}, true
					}
					run := f.capabilityTurn(mode, []string{"bot_skill_save"})
					if run.Status != store.RunSucceeded || run.Report.Skill != nil || run.Report.Quality != nil || f.pool.calls != 0 {
						t.Fatalf("optional learning disrupted final: %+v", run)
					}
					list, err := f.repo.(store.BotSkillStore).ListSkills(f.ctx, f.b.ID, store.BotSkillPage{})
					if err != nil || len(list.Skills) != 0 {
						t.Fatalf("rejection created a method: %+v %v", list, err)
					}
					history := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200))
					if !strings.Contains(history, final) || strings.Count(history, "bot-report:"+run.ID.String()) != 1 {
						t.Fatal("useful answer was not published exactly once")
					}
				})
			}
		}
	}
}

func TestBotAgentOptionalWorkSchemaHTTPAcceptance(t *testing.T) {
	for _, mode := range []capabilityHTTPMode{capabilityInteractive, capabilityAutonomous} {
		for _, name := range []string{"bot_work_read", "bot_work_update", "bot_evidence_read", "bot_check_work"} {
			t.Run(string(mode)+"/"+name, func(t *testing.T) {
				f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, store.BotScopePersonal)
				defer f.worker.Stop()
				steps := 0
				const final = "A malformed optional request did not prevent this useful answer."
				f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
					steps++
					part := &genai.Part{Text: final}
					if steps == 1 {
						part = capabilityHTTPCall(mode, "malformed-optional", name, map[string]any{"unexpected": true})
					} else {
						raw, _ := json.Marshal(req.Contents)
						if !strings.Contains(string(raw), `"status":"invalid_request"`) {
							t.Fatalf("missing recoverable schema feedback: %s", raw)
						}
						work := f.model.work()
						if len(work.AgentWork.Checks) != 0 || len(work.AgentWork.Items) != 0 || work.Review != nil {
							t.Fatal("malformed request mutated work or certified a check")
						}
					}
					return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}, true
				}
				run := f.capabilityTurn(mode, []string{name})
				if run.Status != store.RunSucceeded || steps != 2 || f.pool.calls != 0 {
					t.Fatalf("optional schema error stopped work: %+v", run)
				}
				history := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200))
				if !strings.Contains(history, final) || strings.Count(history, "bot-report:"+run.ID.String()) != 1 {
					t.Fatal("useful final missing or duplicated")
				}
			})
		}
	}
}

// botAgentLearningTurn uses a new HTTP admission identity for each assignment.
func botAgentLearningTurn(t *testing.T, f *interactiveHTTPFixture, mode capabilityHTTPMode, key string, instructions ...string) *store.BotRun {
	t.Helper()
	text := "Use the requested tool"
	if len(instructions) > 0 {
		text = instructions[0]
	}
	body := fmt.Sprintf(`{"version":%d,"instructions":%q,"idempotency_key":%q}`, f.b.Version, text, key)
	var admitted store.BotRun
	if mode == capabilityInteractive {
		var receipt store.BotMessageReceipt
		if err := json.Unmarshal(f.request("POST", f.path+"/messages", body, 202), &receipt); err != nil {
			t.Fatal(err)
		}
		admitted = receipt.Run
	} else if err := json.Unmarshal(f.request("POST", f.path+"/runs", body, 202), &admitted); err != nil {
		t.Fatal(err)
	}
	if err := f.worker.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, err := f.repo.GetRun(f.ctx, f.b.ID, admitted.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// Decode the real producer prompt, not the preparation request or a seeded pin.
func botSkillProducerPin(t *testing.T, req *model.LLMRequest) store.BotSkillRevision {
	t.Helper()
	for _, part := range req.Config.SystemInstruction.Parts {
		start := strings.Index(part.Text, `{"pinned_contract":`)
		if start < 0 {
			continue
		}
		var input struct {
			Contract store.BotWorkContract `json:"pinned_contract"`
		}
		// Prompt escaping turns leaf object braces into angle brackets to avoid
		// ADK interpolation. JSON string values escape literal angle brackets.
		line := strings.SplitN(part.Text[start:], "\n", 2)[0]
		line = strings.NewReplacer("<", "{", ">", "}").Replace(line)
		if err := json.Unmarshal([]byte(line), &input); err != nil {
			t.Fatal(err)
		}
		if input.Contract.Skill == nil {
			t.Fatal("producer lacks skill pin")
		}
		return input.Contract.Skill.Snapshot
	}
	t.Fatal("producer lacks complete pinned contract JSON")
	return store.BotSkillRevision{}
}

// Read the latest actual function response delivered to the producer.
func botSkillProducerBody(t *testing.T, req *model.LLMRequest) string {
	t.Helper()
	var content func(any) string
	content = func(value any) string {
		switch v := value.(type) {
		case map[string]any:
			if body, ok := v["content"].(string); ok {
				return body
			}
			if text, ok := v["text"].(string); ok {
				var decoded any
				if json.Unmarshal([]byte(text), &decoded) == nil {
					return content(decoded)
				}
			}
			for _, child := range v {
				if body := content(child); body != "" {
					return body
				}
			}
		case []any:
			for _, child := range v {
				if body := content(child); body != "" {
					return body
				}
			}
		}
		return ""
	}
	for i := len(req.Contents) - 1; i >= 0; i-- {
		for _, part := range req.Contents[i].Parts {
			if part.FunctionResponse != nil {
				raw, _ := json.Marshal(part.FunctionResponse.Response)
				var decoded any
				_ = json.Unmarshal(raw, &decoded)
				if body := content(decoded); body != "" {
					return body
				}
			}
		}
	}
	t.Fatal("producer lacks actual tool response bytes")
	return ""
}

// HTTP admission and readback use the production worker, factory and ChatRunner.
// Only the model and sandbox tool transport are controlled; skills are not seeded.
func TestBotSkillRuntimeHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, scenario := range []string{"format-recovery", "draft-before-tools", "reuse", "feedback", "greeting", "malformed", "sensitive-content", "revoked-orientation", "source-learning-reuse", "method-learning", "learning-rejected"} {
			t.Run(string(scope)+"/"+scenario, func(t *testing.T) {
				toolName := "read_file"
				args := map[string]any{"path": "/workspace/receipt.txt"}
				var retrievals atomic.Int32
				if scenario == "source-learning-reuse" || scenario == "format-recovery" {
					source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "text/plain")
						_, _ = fmt.Fprintf(w, "receipt-%d", retrievals.Add(1))
					}))
					defer source.Close()
					toolName = "web_fetch"
					args = map[string]any{"url": source.URL, "method": "GET"}
				}
				f := newInteractiveHTTPFixture(t, toolName, args, []string{"chat", "tool:execute"}, scope)
				defer f.worker.Stop()
				if scenario == "source-learning-reuse" || scenario == "format-recovery" {
					f.pool.callOverride = func(name string, actual map[string]interface{}) (json.RawMessage, error) {
						if name != "web_fetch" || actual["url"] != args["url"] {
							return nil, fmt.Errorf("unexpected source request %s", name)
						}
						response, err := http.Get(args["url"].(string))
						if err != nil {
							return nil, err
						}
						defer response.Body.Close()
						body, err := io.ReadAll(response.Body)
						if err != nil {
							return nil, err
						}
						return json.Marshal(map[string]any{"final_url": response.Request.URL.String(), "content": string(body), "status": response.StatusCode})
					}
				}
				skills := f.repo.(store.BotSkillStore)
				original := f.model.assessment
				orientations, learnings := 0, 0
				var orientationSchema, learningSchema string
				var selected string
				assignment := "Prepare a receipt report"
				if scenario == "feedback" {
					assignment += ". Use Celsius"
				}
				if scenario == "greeting" {
					assignment = "Hello"
					f.model.called = true
				}
				f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
					prompt := ""
					if req.Config != nil && req.Config.SystemInstruction != nil {
						prompt = req.Config.SystemInstruction.Parts[0].Text
					}
					if strings.HasPrefix(prompt, "BOT_SKILL_LEARN_V1") {
						learnings++
						if scenario == "format-recovery" {
							schema, _ := json.Marshal(req.Config.ResponseJsonSchema)
							if learnings == 1 {
								learningSchema = string(schema)
								return &model.LLMResponse{Content: genai.NewContentFromText("{", "model")}, true
							}
							if learnings != 2 || string(schema) != learningSchema {
								t.Fatal("learning correction schema or bound changed")
							}
						}
						if len(req.Tools) != 0 || req.Config.ResponseJsonSchema == nil {
							t.Fatal("learning assessment is not bounded")
						}
						if scenario == "method-learning" || scenario == "learning-rejected" {
							var work store.BotWorkState
							if err := json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &work); err != nil || work.Candidate == nil || len(work.Candidate.Observations) == 0 {
								t.Fatal("learning lacks actual candidate")
							}
							ref := "observation:0"
							if scenario == "learning-rejected" {
								ref = "observation:9999"
							}
							raw, _ := json.Marshal(map[string]any{"method": []string{"Read the complete file before reporting its contents"}, "output_format": "Plain text report", "evaluation_rules": []string{"Compare report against captured file content"}, "evidence_refs": []string{ref}, "reason": "Observed complete file retrieval"})
							return &model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, true
						}
						return &model.LLMResponse{Content: genai.NewContentFromText(`{"method":[],"output_format":"","evaluation_rules":[],"evidence_refs":[],"reason":""}`, "model")}, true
					}
					if strings.HasPrefix(prompt, "BOT_SKILL_ORIENT_V1") {
						orientations++
						if f.pool.calls != 0 && orientations == 1 {
							t.Fatal("tools ran before orientation")
						}
						if len(req.Tools) != 0 {
							t.Fatal("orientation has tools")
						}
						if req.Config.ResponseJsonSchema == nil {
							t.Fatal("orientation lacks strict schema")
						}
						if scenario == "format-recovery" {
							schema, _ := json.Marshal(req.Config.ResponseJsonSchema)
							if f.pool.calls != 0 {
								t.Fatal("tools ran before correction completed")
							}
							if orientations == 1 {
								orientationSchema = string(schema)
								return &model.LLMResponse{Content: genai.NewContentFromText("{} {}", "model")}, true
							}
							if orientations != 2 || string(schema) != orientationSchema {
								t.Fatal("orientation correction schema or bound changed")
							}
						}
						if scenario == "malformed" {
							return &model.LLMResponse{Content: genai.NewContentFromText("{} {}", "model")}, true
						}
						if scenario == "revoked-orientation" {
							f.registry.Close()
						}
						kind := "create_draft"
						quotes := []string{assignment}
						if selected != "" {
							kind = "use_existing"
						}
						replaceIDs := []string{}
						if scenario == "feedback" && selected != "" {
							kind = "update_owner_expectations"
							quotes = []string{"Use Fahrenheit"}
							var input struct {
								Expectations map[string][]store.BotSkillExpectation `json:"skill_expectations"`
							}
							if err := json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &input); err != nil {
								t.Fatal(err)
							}
							for _, e := range input.Expectations[selected] {
								if e.Text == "Use Celsius" || e.Text == "Use Fahrenheit" {
									replaceIDs = append(replaceIDs, e.ID)
								}
							}
							if len(replaceIDs) != 1 {
								t.Fatal("orientation lacks exact replaceable expectation IDs")
							}
						}
						if scenario == "feedback" && selected == "" {
							quotes = []string{"Prepare a receipt report", "Use Celsius"}
						}
						if scenario == "greeting" {
							kind = "no_skill_needed"
						}
						method := []string{"Read the receipt", "Report the receipt truthfully"}
						if scenario == "sensitive-content" {
							method = []string{"Use https://example.test/?token=private"}
						}
						raw, _ := json.Marshal(map[string]any{"kind": kind, "skill_id": selected, "title": "Receipt report", "responsibility": "Report receipt content", "method": method, "expectations": quotes, "replace_expectation_ids": replaceIDs, "reason": "Owner assignment"})
						return &model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, true
					}
					if strings.HasPrefix(prompt, "BOT_WORK_PREPARE_V1") && scenario != "greeting" {
						list, err := skills.ListSkills(f.ctx, f.b.ID, store.BotSkillPage{})
						if err != nil || len(list.Skills) != 1 {
							t.Fatalf("draft not durable before execution: %+v %v", list, err)
						}
						selected = list.Skills[0].ID.String()
						if scenario == "source-learning-reuse" && orientations == 2 {
							var contract store.BotWorkContract
							if err := json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &contract); err != nil {
								t.Fatal(err)
							}
							if contract.Skill == nil || len(contract.Skill.Snapshot.Sources) != 1 || contract.Skill.Snapshot.Sources[0].URL != args["url"] || contract.Skill.Snapshot.Sources[0].Provenance.Level != store.BotSkillObserved {
								t.Fatalf("reused pin lacks observed source: %+v", contract.Skill)
							}
						}
						if !strings.Contains(req.Contents[0].Parts[0].Text, selected) {
							t.Fatal("assessment lacks pinned skill")
						}
					}
					if scenario == "greeting" && strings.HasPrefix(prompt, "BOT_WORK_REVIEW_V1") {
						var w store.BotWorkState
						_ = json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &w)
						raw, _ := json.Marshal(store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "scoped-result", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + w.Candidate.Digest}, Explanation: "Greeting answered"}}})
						return &model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, true
					}
					if scenario == "feedback" || scenario == "source-learning-reuse" || scenario == "format-recovery" {
						if strings.HasPrefix(prompt, "BOT_WORK_REVIEW_V1") {
							var work store.BotWorkState
							if err := json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &work); err != nil {
								t.Fatal(err)
							}
							want := "isolated receipt | Celsius"
							if scenario == "format-recovery" {
								want = "receipt-1"
							}
							if scenario == "feedback" && orientations > 1 {
								want = "isolated receipt | Fahrenheit"
							}
							if scenario == "source-learning-reuse" {
								want = fmt.Sprintf("receipt-%d", orientations)
							}
							if work.Candidate.Text != want || len(work.Candidate.Observations) == 0 {
								t.Fatalf("producer did not obey current standard and bytes: %+v, want %q", work.Candidate, want)
							}
							raw, _ := json.Marshal(store.BotWorkReview{CandidateDigest: work.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "scoped-result", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"observation:0"}, Explanation: "Compared exact output to current observed bytes and pinned owner standard"}}})
							return &model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, true
						}
						if !strings.HasPrefix(prompt, "BOT_") && f.model.called {
							body := botSkillProducerBody(t, req)
							if scenario == "feedback" {
								pin := botSkillProducerPin(t, req)
								units := ""
								for _, e := range pin.Expectations {
									switch e.Text {
									case "Use Celsius":
										if units != "" {
											t.Fatal("conflicting units")
										}
										units = "Celsius"
									case "Use Fahrenheit":
										if units != "" {
											t.Fatal("conflicting units")
										}
										units = "Fahrenheit"
									}
								}
								if units == "" {
									t.Fatal("producer lacks owner units")
								}
								body += " | " + units
							}
							return &model.LLMResponse{Content: genai.NewContentFromText(body, "model")}, true
						}
					}
					return original(req)
				}
				turns := 1
				if scenario == "reuse" || scenario == "feedback" || scenario == "source-learning-reuse" || scenario == "method-learning" {
					turns = 2
				}
				if scenario == "feedback" {
					turns = 3
				}
				for turn := 0; turn < turns; turn++ {
					if turn > 0 && scenario == "feedback" {
						assignment = "Use Fahrenheit"
					}
					f.model.called = scenario == "greeting"
					raw := f.request("POST", f.path+"/messages", fmt.Sprintf(`{"version":%d,"instructions":%q,"idempotency_key":"skill-%d"}`, f.b.Version, assignment, turn), 202)
					var receipt store.BotMessageReceipt
					if err := json.Unmarshal(raw, &receipt); err != nil {
						t.Fatal(err)
					}
					// Preserve mandatory orientation and review for historical runs only.
					pinLegacyHTTPAttempt(t, f, receipt.Run)
					if err := f.worker.Poll(f.ctx); err != nil {
						t.Fatal(err)
					}
					var run store.BotRun
					if err := json.Unmarshal(f.request("GET", f.path+"/runs/"+receipt.Run.ID.String(), "", 200), &run); err != nil {
						t.Fatal(err)
					}
					negative := scenario == "malformed" || scenario == "sensitive-content" || scenario == "revoked-orientation"
					if negative {
						if run.Status == store.RunSucceeded || f.pool.calls != 0 {
							t.Fatalf("unsafe execution: %+v", run)
						}
						continue
					}
					if run.Status != store.RunSucceeded {
						t.Fatalf("run failed: %+v", run.Report)
					}
					if scenario != "greeting" && (run.Report.Skill == nil || run.Report.Skill.SkillID.String() != selected) {
						t.Fatalf("HTTP receipt not canonical: %+v", run.Report.Skill)
					}
					if scenario == "method-learning" && turn == 0 && (run.Report.Skill.Kind != store.BotSkillUpdated || run.Report.Skill.CommittedRevision != 2) {
						t.Fatalf("general method was not learned: %+v", run.Report.Skill)
					}
					if scenario == "method-learning" && turn == 1 && (run.Report.Skill.UsedRevision != 2 || run.Report.Skill.Kind == store.BotSkillUpdated) {
						t.Fatalf("identical method produced an update receipt: %+v", run.Report.Skill)
					}
					if scenario == "learning-rejected" && run.Report.Skill.Kind != store.BotSkillRejected {
						t.Fatalf("missing durable learning rejection: %+v", run.Report.Skill)
					}
					if scenario == "source-learning-reuse" {
						if turn == 0 && (run.Report.Skill.Kind != store.BotSkillUpdated || run.Report.Skill.UsedRevision != 1 || run.Report.Skill.CommittedRevision != 2) {
							t.Fatalf("actual HTTP source not committed atomically: %+v", run.Report.Skill)
						}
						if turn == 1 && run.Report.Skill.CommittedRevision != 3 {
							t.Fatalf("fresh access to existing URL was not retained: %+v", run.Report.Skill)
						}
						if turn == 1 && run.Report.Skill.UsedRevision != 2 {
							t.Fatalf("learned source revision not reused: %+v", run.Report.Skill)
						}
					}
					if scenario == "feedback" && turn > 0 && run.Report.Skill.UsedRevision != 2 {
						t.Fatal("feedback did not pin owner update")
					}
					if scenario == "feedback" || scenario == "source-learning-reuse" || scenario == "format-recovery" {
						want := "isolated receipt | Celsius"
						if scenario == "format-recovery" {
							want = "receipt-1"
						}
						if scenario == "feedback" && turn > 0 {
							want = "isolated receipt | Fahrenheit"
						}
						if scenario == "source-learning-reuse" {
							want = fmt.Sprintf("receipt-%d", turn+1)
						}
						if run.Report.Summary != want {
							t.Fatalf("published output=%q want=%q", run.Report.Summary, want)
						}
					}
				}
				wantOrientations := turns
				if scenario == "malformed" || scenario == "format-recovery" {
					wantOrientations++
				}
				if orientations != wantOrientations {
					t.Fatalf("orientation calls=%d want %d", orientations, wantOrientations)
				}
				list, err := skills.ListSkills(f.ctx, f.b.ID, store.BotSkillPage{})
				if err != nil {
					t.Fatal(err)
				}
				want := 1
				if scenario == "greeting" || scenario == "malformed" || scenario == "sensitive-content" || scenario == "revoked-orientation" {
					want = 0
				}
				if len(list.Skills) != want {
					t.Fatalf("skill count=%d want=%d", len(list.Skills), want)
				}
				if want == 1 && list.Skills[0].Status != store.BotSkillActive {
					t.Fatalf("accepted use did not activate skill: %+v", list.Skills[0])
				}
				if scenario == "format-recovery" {
					if retrievals.Load() != 1 || f.pool.calls != 1 || learnings != 2 {
						t.Fatalf("replayed work: HTTP=%d tools=%d learning=%d", retrievals.Load(), f.pool.calls, learnings)
					}
					head, err := skills.GetSkillRevision(f.ctx, f.b.ID, list.Skills[0].ID, 2)
					if err != nil || len(head.Sources) != 1 || head.Sources[0].URL != args["url"] || head.Sources[0].Provenance.Level != store.BotSkillObserved {
						t.Fatalf("recovered skill missing observed source: %+v %v", head, err)
					}
					if err := f.worker.Poll(f.ctx); err != nil {
						t.Fatal(err)
					}
					if retrievals.Load() != 1 || f.pool.calls != 1 {
						t.Fatal("terminal recovery replayed task")
					}
					for range 2 {
						history := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200))
						if strings.Count(history, `"text":"receipt-1"`) != 1 {
							t.Fatalf("expected exactly one delivered answer: %s", history)
						}
					}
				}
				if scenario == "method-learning" {
					if list.Skills[0].HeadRevision != 2 {
						t.Fatal("identical learned method inserted a new revision")
					}
					head, err := skills.GetSkillRevision(f.ctx, f.b.ID, list.Skills[0].ID, 2)
					if err != nil {
						t.Fatal(err)
					}
					if len(head.Method) != 1 || head.Method[0] != "Read the complete file before reporting its contents" || head.OutputFormat != "Plain text report" || len(head.EvaluationRules) != 1 || head.EvaluationRules[0] != "Compare report against captured file content" {
						t.Fatalf("generalized advisory learning missing: %+v", head)
					}
					if len(head.Expectations) != 1 || head.Expectations[0].Text != "Prepare a receipt report" {
						t.Fatal("advisory learning changed owner requirements")
					}
				}
				if scenario == "feedback" && list.Skills[0].HeadRevision != 2 {
					t.Fatal("repeat feedback inserted another revision")
				}
				if scenario == "source-learning-reuse" {
					if retrievals.Load() != 2 {
						t.Fatalf("expected exactly two fresh HTTP requests, got %d", retrievals.Load())
					}
					prior, err := skills.GetSkillRevision(f.ctx, f.b.ID, list.Skills[0].ID, 2)
					if err != nil {
						t.Fatal(err)
					}
					current, err := skills.GetSkillRevision(f.ctx, f.b.ID, list.Skills[0].ID, 3)
					if err != nil {
						t.Fatal(err)
					}
					if len(prior.Sources) != 1 || len(current.Sources) != 1 {
						t.Fatal("refresh duplicated source")
					}
					if !current.Sources[0].LastObservedAt.After(*prior.Sources[0].LastObservedAt) || current.Sources[0].Provenance.RunID == prior.Sources[0].Provenance.RunID {
						t.Fatal("refresh lost distinct observation history")
					}
					if current.Sources[0].Limitation != "" {
						t.Fatal("successful source access retained limitation")
					}
				}
			})
		}
	}
}
