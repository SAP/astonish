package api_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// pinLegacyHTTPAttempt restores only the protocol pin of a historical queued
// attempt in this fixture's owned SQLite file, before the worker has started.
// Admission, authority, execution, assessment and publication still use production
// stores and factories. No candidate or verdict is seeded, and production cannot
// downgrade a run through SaveWork.
func pinLegacyHTTPAttempt(t *testing.T, f *interactiveHTTPFixture, run store.BotRun) {
	t.Helper()
	path := filepath.Join(f.dataDir, "orgs", "interactive", "teams", "team.db")
	if f.scope == store.BotScopePersonal {
		path = filepath.Join(f.dataDir, "orgs", "interactive", "personal", f.b.OwnerID.String()+".db")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw []byte
	if err := db.QueryRowContext(f.ctx, "SELECT data FROM bot_runs WHERE id = ? AND bot_id = ? AND status = 'queued'", run.ID.String(), run.BotID.String()).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var storage map[string]string
	if err := json.Unmarshal(raw, &storage); err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(storage["bot_document"]), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope["work"]) != 0 {
		t.Fatal("fixture attempt already has work")
	}
	input, err := f.repo.(store.BotRuntimeStore).LoadRunInput(f.ctx, run.BotID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	work := store.NewBotWorkState(store.BotWorkContract{BotID: run.BotID, RunID: run.ID, Version: run.Version, Instructions: input.Instructions, Charter: input.Version.Charter})
	if err := work.Validate(); err != nil {
		t.Fatal(err)
	}
	envelope["work"], err = json.Marshal(work)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	storage["bot_document"] = string(updated)
	updated, err = json.Marshal(storage)
	if err != nil {
		t.Fatal(err)
	}
	result, err := db.ExecContext(f.ctx, "UPDATE bot_runs SET data = ? WHERE id = ? AND bot_id = ? AND status = 'queued' AND CAST(data AS TEXT) = ?", string(updated), run.ID.String(), run.BotID.String(), string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("legacy fixture pin: rows=%d err=%v", n, err)
	}
}

// Actual HTTP admission/readback, scoped SQLite, production worker, factory and
// ChatRunner, with an explicit historical protocol pin. No verdicts are seeded.
func TestBotWorkPublicationHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "read_file", nil, []string{"chat", "tool:execute"}, scope)
			defer f.worker.Stop()
			f.model.qualityMode = "publication"
			historyPath := f.path + "/sessions/" + f.b.HomeSessionID.String() + "/stream"
			finalText := "Corrected: the receipt says isolated receipt."
			f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
				if req.Config == nil || req.Config.SystemInstruction == nil {
					return nil, false
				}
				prompt := req.Config.SystemInstruction.Parts[0].Text
				var output any
				switch {
				case strings.HasPrefix(prompt, "BOT_SKILL_ORIENT_V1"):
					output = map[string]any{"kind": "no_skill_needed", "skill_id": "", "title": "", "responsibility": "", "method": []string{}, "expectations": []string{}, "reason": "Controlled publication fixture"}
				case strings.HasPrefix(prompt, "BOT_WORK_PREPARE_V1"):
					output = map[string]any{"criteria": []store.BotWorkCriterion{{ID: "content", SourceIDs: []string{"assignment", "charter"}, Text: "Return the owner supplied receipt text"}}}
				case strings.HasPrefix(prompt, "BOT_WORK_REVIEW_V1"):
					var work store.BotWorkState
					if err := json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &work); err != nil {
						t.Fatal(err)
					}
					// Read the actual HTTP Home while the producer has finished but
					// its candidate is still being assessed. Drafts must not be there.
					history := string(f.request("GET", historyPath, "", 200))
					if strings.Contains(history, "Defective: the receipt says imaginary.") {
						t.Fatal("draft leaked before final commit")
					}
					verdict := store.BotCriterionFailed
					if work.Candidate.Text == finalText {
						verdict = store.BotCriterionSatisfied
					}
					output = store.BotWorkReview{CandidateDigest: work.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "content", Verdict: verdict, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + work.Candidate.Digest}, Explanation: "Use the supplied exact receipt"}}}
				default:
					return nil, false
				}
				raw, _ := json.Marshal(output)
				return &model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, true
			}
			var ids []string
			for i := 0; i < 3; i++ {
				f.model.productions = 0
				var receipt store.BotMessageReceipt
				raw := f.request("POST", f.path+"/messages", fmt.Sprintf(`{"version":%d,"instructions":"Report the supplied text: isolated receipt.","idempotency_key":"publication-%d"}`, f.b.Version, i), 202)
				if err := json.Unmarshal(raw, &receipt); err != nil {
					t.Fatal(err)
				}
				pinLegacyHTTPAttempt(t, f, receipt.Run)
				if err := f.worker.Poll(f.ctx); err != nil {
					t.Fatal(err)
				}
				var run store.BotRun
				if err := json.Unmarshal(f.request("GET", f.path+"/runs/"+receipt.Run.ID.String(), "", 200), &run); err != nil {
					t.Fatal(err)
				}
				if run.Status != store.RunSucceeded || run.Report.Quality == nil || run.Report.Quality.Outcome != store.BotWorkAccepted || run.Report.Summary != finalText {
					t.Fatalf("not assessed: %+v", run)
				}
				ids = append(ids, "bot-report:"+run.ID.String())
			}
			history := string(f.request("GET", historyPath, "", 200))
			if strings.Contains(history, "Defective: the receipt says imaginary.") {
				t.Fatal("rejected candidate restored into Home")
			}
			for _, id := range ids {
				if strings.Count(history, id) != 1 {
					t.Fatalf("publication not independently visible once: %s count=%d", id, strings.Count(history, id))
				}
			}
			if strings.Count(history, finalText) != 3 {
				t.Fatalf("want three distinct final reports, got %d", strings.Count(history, finalText))
			}
			// Reconciliation/reload cannot publish the same committed result twice.
			if err := f.worker.Poll(f.ctx); err != nil {
				t.Fatal(err)
			}
			reloaded := string(f.request("GET", historyPath, "", 200))
			for _, id := range ids {
				if strings.Count(reloaded, id) != 1 {
					t.Fatal("duplicate after reconcile/reload", id)
				}
			}
		})
	}
}

func TestBotWorkRuntimeHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, mode := range []string{"fail-repair-pass", "limits", "malformed", "forged-verdict", "missing-evidence", "provider-error", "trailing-json", "revoked-during-review"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				f := newInteractiveHTTPFixture(t, "read_file", nil, []string{"chat", "tool:execute"}, scope)
				defer f.worker.Stop()
				f.model.qualityMode = mode
				f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
					if req.Config == nil || req.Config.SystemInstruction == nil {
						return nil, false
					}
					prompt := req.Config.SystemInstruction.Parts[0].Text
					var output any
					if strings.HasPrefix(prompt, "BOT_SKILL_ORIENT_V1") {
						output = map[string]any{"kind": "no_skill_needed", "skill_id": "", "title": "", "responsibility": "", "method": []string{}, "expectations": []string{}, "reason": "Controlled quality fixture"}
					} else if strings.HasPrefix(prompt, "BOT_EMPLOYEE_MESSAGE") {
						var source struct {
							SourceDigest string `json:"source_digest"`
						}
						if err := json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &source); err != nil {
							t.Fatal(err)
						}
						output = map[string]any{"source_digest": source.SourceDigest, "choice": 0}
					} else if strings.HasPrefix(prompt, "BOT_WORK_PREPARE_V1") {
						var contract store.BotWorkContract
						if err := json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &contract); err != nil {
							t.Fatal(err)
						}
						if !strings.Contains(contract.Instructions, "isolated receipt") {
							t.Fatal("original assignment lost")
						}
						output = map[string]any{"criteria": []store.BotWorkCriterion{{ID: "content", SourceIDs: []string{"assignment", "charter"}, Text: "Report the exact supplied receipt, isolated receipt; do not invent content."}}}
					} else if strings.HasPrefix(prompt, "BOT_WORK_REVIEW_V1") {
						f.model.reviews++
						var work store.BotWorkState
						if err := json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &work); err != nil {
							t.Fatal(err)
						}
						if work.Phase != store.BotWorkReviewing || work.Candidate.Digest != work.Candidate.ContentDigest() {
							t.Fatal("review not durably bound to captured candidate")
						}
						if mode == "revoked-during-review" {
							f.registry.Close()
							return &model.LLMResponse{Content: genai.NewContentFromText("{}", "model")}, true
						}
						if mode == "provider-error" {
							return &model.LLMResponse{ErrorCode: "provider_error", ErrorMessage: "review unavailable", Content: genai.NewContentFromText("{}", "model")}, true
						}
						if mode == "trailing-json" {
							return &model.LLMResponse{Content: genai.NewContentFromText("{} {}", "model")}, true
						}
						if mode == "malformed" {
							return &model.LLMResponse{Content: genai.NewContentFromText("not-json", "model")}, true
						}
						verdict := store.BotCriterionFailed
						if work.Candidate.Text == "Corrected: the receipt says isolated receipt." {
							verdict = store.BotCriterionSatisfied
						}
						refs := []string{"candidate:" + work.Candidate.Digest}
						method := store.BotAssessmentModel
						if mode == "forged-verdict" {
							method = store.BotAssessmentMechanical
						}
						if mode == "missing-evidence" {
							verdict = store.BotCriterionSatisfied
							refs = []string{"observation:999"}
						}
						output = store.BotWorkReview{CandidateDigest: work.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "content", Verdict: verdict, Method: method, EvidenceRefs: refs, Explanation: "Replace imaginary with the supplied exact text isolated receipt."}}}
					} else {
						return nil, false
					}
					raw, _ := json.Marshal(output)
					return &model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, true
				}
				var receipt store.BotMessageReceipt
				raw := f.request("POST", f.path+"/messages", fmt.Sprintf(`{"version":%d,"instructions":"Inspect and correct the supplied defective draft: imaginary. The correct receipt text supplied by the owner is isolated receipt.","idempotency_key":"quality"}`, f.b.Version), 202)
				if err := json.Unmarshal(raw, &receipt); err != nil {
					t.Fatal(err)
				}
				pinLegacyHTTPAttempt(t, f, receipt.Run)
				if err := f.worker.Poll(f.ctx); err != nil {
					t.Fatal(err)
				}
				var run store.BotRun
				if err := json.Unmarshal(f.request("GET", f.path+"/runs/"+receipt.Run.ID.String(), "", 200), &run); err != nil {
					t.Fatal(err)
				}
				if mode == "fail-repair-pass" {
					if run.Status != store.RunSucceeded || run.Report.Summary != "Corrected: the receipt says isolated receipt." || run.Report.Quality == nil || run.Report.Quality.Outcome != store.BotWorkAccepted || run.Report.Quality.Repairs != 1 || f.model.productions != 2 || f.model.reviews != 2 {
						t.Fatalf("repair did not deliver assessed output: %+v productions=%d reviews=%d", run, f.model.productions, f.model.reviews)
					}
				} else {
					if run.Status == store.RunSucceeded || strings.Contains(run.Report.Summary, "Corrected:") || (run.Report.Quality != nil && run.Report.Quality.Outcome == store.BotWorkAccepted) {
						t.Fatalf("unsafe success: %+v", run)
					}
					wantReviews := 1
					if mode == "malformed" || mode == "trailing-json" {
						wantReviews = 2
					}
					if mode != "limits" && (f.model.productions != 1 || f.model.reviews != wantReviews) {
						t.Fatalf("invalid review was replayed: productions=%d reviews=%d", f.model.productions, f.model.reviews)
					}
					if len(run.Report.Artifacts) != 0 {
						t.Fatal("unaccepted draft artifacts were published")
					}
					if mode == "limits" && (f.model.productions != 3 || f.model.reviews != 3 || run.Report.Quality.Repairs != 2) {
						t.Fatalf("unbounded repair: %+v", run)
					}
				}
				if f.pool.calls != 0 {
					t.Fatal("assessment obtained unauthorized tool authority")
				}
			})
		}
	}
}
