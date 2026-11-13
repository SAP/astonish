package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

const blockedPublicationAssignment = "can you update it to send via e-mail too ?"

const blockedPublicationText = "I couldn't add email delivery because the email transport is unavailable. An administrator needs to enable the email channel. I haven't changed the report's delivery settings."

// Only the model and sandbox are controlled. Transport evidence comes from the
// production worker resolver, account/channel storage and assessment capture.
type blockedPublicationModel struct {
	rejectExplanation                  bool
	t                                  *testing.T
	preparations, productions, reviews int
}

func (*blockedPublicationModel) Name() string { return "blocked-publication" }
func (m *blockedPublicationModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		prompt := req.Config.SystemInstruction.Parts[0].Text
		var out any
		switch {
		case strings.HasPrefix(prompt, "BOT_SKILL_ORIENT_V1"):
			out = map[string]any{"kind": store.BotSkillNoSkillNeeded, "skill_id": "", "title": "", "responsibility": "", "method": []string{}, "expectations": []string{}, "reason": "Controlled blocker explanation fixture"}
		case strings.HasPrefix(prompt, "BOT_WORK_PREPARE_V1"):
			m.preparations++
			out = map[string]any{"criteria": []store.BotWorkCriterion{
				{ID: "c1", SourceIDs: []string{"assignment"}, Text: "Add email delivery", FulfillmentScope: store.BotWorkCriterionFutureTaskDefinition},
				{ID: "c2", SourceIDs: []string{"charter"}, Text: "Give useful truthful feedback"},
			}}
		case strings.HasPrefix(prompt, "BOT_WORK_REVIEW_V1"):
			m.reviews++
			var w store.BotWorkState
			if err := json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &w); err != nil {
				m.t.Fatal(err)
			}
			if w.Candidate == nil || w.Candidate.Text != blockedPublicationText || len(w.Candidate.RuntimeBlockers) != 1 || len(w.Candidate.Observations) == 0 || !strings.Contains(prompt, "BOT_WORK_BLOCKED_RESPONSE_V1") {
				m.t.Fatalf("review lacks exact candidate/trusted blocker: %+v", w.Candidate)
			}
			raw, _ := json.Marshal(w.Candidate.Bodies)
			if !strings.Contains(string(raw), "transport_unavailable") {
				m.t.Fatal("runtime transport observation missing")
			}
			out = map[string]any{
				"candidate_digest": w.Candidate.Digest,
				"criteria": []store.BotCriterionAssessment{
					{CriterionID: "c1", Verdict: store.BotCriterionFailed, Method: store.BotAssessmentModel, Explanation: "REVIEWER_ONLY: email delivery was not added"},
					{CriterionID: "c2", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + w.Candidate.Digest}, Explanation: "Useful truthful blocker explanation"},
				},
				"blocked_response": map[string]any{"blocker_id": "owner-email-transport", "criterion_ids": []string{"c1"}, "source_id": "assignment", "source_quote": blockedPublicationAssignment, "dependency_purpose": "configure_delivery", "dependency_verdict": "satisfied", "verdict": "satisfied", "method": "model_assessment", "explanation": "The exact employee response identifies the unavailable transport, administrator action and unchanged settings without claiming success."},
			}
			if m.rejectExplanation {
				out.(map[string]any)["blocked_response"].(map[string]any)["verdict"] = "failed"
			}
		case strings.HasPrefix(prompt, "BOT_EMPLOYEE_MESSAGE"):
			out = map[string]any{}
		default:
			m.productions++
			raw, _ := json.Marshal(req.Contents)
			if !strings.Contains(string(raw), blockedPublicationAssignment) {
				m.t.Fatal("original user assignment lost")
			}
			yield(&model.LLMResponse{Content: genai.NewContentFromText(blockedPublicationText, "model")}, nil)
			return
		}
		if len(req.Tools) != 0 {
			m.t.Fatal("assessor has execution tools")
		}
		raw, err := json.Marshal(out)
		if err != nil {
			m.t.Fatal(err)
		}
		response := &model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}
		fixtureHTTPContentCoverage(req, response)
		yield(response, nil)
	}
}

type blockedPublicationFrame struct{ id, kind, data string }

func readBlockedPublicationFrame(t *testing.T, r *bufio.Reader) blockedPublicationFrame {
	t.Helper()
	var f blockedPublicationFrame
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			return f
		}
		switch {
		case strings.HasPrefix(line, "id: "):
			f.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			f.kind = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			f.data = strings.TrimPrefix(line, "data: ")
		}
	}
}

// Real HTTP admission -> production worker/ChatRunner -> SQLite commit -> Home
// SSE live/replay and history. No live user data, external provider or mail send.
func TestBotBlockedExplanationPublicationHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, rejectExplanation := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reject=%v", scope, rejectExplanation), func(t *testing.T) {
				f := newInteractiveHTTPFixture(t, "read_file", nil, []string{"chat", "tool:execute"}, scope)
				owner := f.b.OwnerID.String()
				link := &store.UserChannel{ID: uuid.NewString(), UserID: owner, ChannelType: store.BotDeliveryChannelEmail, ExternalID: owner + "@example.test", Enabled: true}
				if err := f.db.UserChannels().Link(f.ctx, link); err != nil {
					t.Fatal(err)
				}
				if err := f.db.UserChannels().Verify(f.ctx, link.ID); err != nil {
					t.Fatal(err)
				}
				llm := &blockedPublicationModel{t: t, rejectExplanation: rejectExplanation}
				var err error
				f.worker, err = daemon.NewBotWorker(daemon.BotWorkerConfig{
					Backend: f.db, Sandbox: journeyHomeBackend{}, Pool: f.pool, TurnRegistry: f.registry, WorkerID: uuid.NewString(), ContextWindow: 1000000,
					ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 1000, SupportsOutputLimit: true},
					ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
						return llm, nil
					},
					Prepare: func(context.Context, bot.RunRequest, *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
						return &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: f.pool}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				open := func(cursor string) *http.Response {
					t.Helper()
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					t.Cleanup(cancel)
					req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.server.URL+f.path+"/sessions/"+f.b.HomeSessionID.String()+"/events?scope="+string(scope), nil)
					if err != nil {
						t.Fatal(err)
					}
					req.Header.Set("Last-Event-ID", cursor)
					res, err := f.server.Client().Do(req)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { res.Body.Close() })
					if res.StatusCode != http.StatusOK {
						t.Fatalf("Home stream status: %d", res.StatusCode)
					}
					return res
				}
				live := open("")
				rd := bufio.NewReader(live.Body)
				// Drain the initial snapshot before the worker can publish anything.
				for readBlockedPublicationFrame(t, rd).kind != "replay_complete" {
				}
				var receipt store.BotMessageReceipt
				body := fmt.Sprintf(`{"version":%d,"instructions":%q,"idempotency_key":"email-blocker"}`, f.b.Version, blockedPublicationAssignment)
				if err := json.Unmarshal(f.request("POST", f.path+"/messages", body, 202), &receipt); err != nil {
					t.Fatal(err)
				}
				// Exercise the historical assessor, not the new agent-led default.
				pinLegacyHTTPAttempt(t, f, receipt.Run)
				if err := f.worker.Poll(context.Background()); err != nil {
					t.Fatal(err)
				}
				run, err := f.repo.GetRun(f.ctx, f.b.ID, receipt.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				q := run.Report.Quality
				if rejectExplanation {
					m := run.Report.EmployeeMessage
					if run.Status != store.RunPartial || q == nil || q.BlockedResponse != nil || q.Repairs != store.BotWorkMaxRepairs || m == nil || m.Origin != store.BotEmployeeMessageFallback || m.Reason != store.BotEmployeeReasonEmailTransport || !strings.Contains(m.Text, "administrator") || len(run.Report.Artifacts) != 0 || f.pool.calls != 0 {
						t.Fatalf("lost safe relevant fallback: run=%+v message=%+v quality=%+v", run, m, q)
					}
					if llm.productions != 3 || llm.reviews != 3 {
						t.Fatalf("unexpected repair counts: %+v", llm)
					}
					var restored store.BotRun
					if err := json.Unmarshal(f.request("GET", f.path+"/runs/"+run.ID.String(), "", 200), &restored); err != nil {
						t.Fatal(err)
					}
					if restored.Report.EmployeeMessage == nil || restored.Report.EmployeeMessage.Text != m.Text || bot.RenderReport(restored) != m.Text+"\n" {
						t.Fatal("fallback lost on reload")
					}
					history := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200))
					if strings.Count(history, m.Text) != 1 || strings.Contains(history, blockedPublicationText) || strings.Contains(history, "REVIEWER_ONLY") {
						t.Fatalf("unsafe fallback history: %s", history)
					}
					return
				}
				if run.Status != store.RunPartial || run.Report.Summary != blockedPublicationText || q == nil || q.Outcome != store.BotWorkBlocked || q.ResponseOutcome != store.BotWorkResponseVerifiedBlocker || q.BlockedResponse == nil || q.BlockedResponse.Text != blockedPublicationText || q.Repairs != 0 || len(q.Criteria) != 2 || q.Criteria[0].Verdict != store.BotCriterionFailed || q.Criteria[1].Verdict != store.BotCriterionSatisfied {
					t.Fatalf("lost exact partial blocker: %+v quality=%+v", run, q)
				}
				if len(run.Report.Unverified) == 0 {
					t.Fatal("test must exercise generic appendix suppression with unresolved original criterion")
				}
				if got := bot.RenderReport(*run); got != blockedPublicationText+"\n" {
					t.Fatalf("terminal report changed employee text: %q", got)
				}
				reportID := "bot-report:" + run.ID.String()
				check := func(frame blockedPublicationFrame) bool {
					t.Helper()
					if frame.kind != "transcript" {
						return false
					}
					var event session.Event
					if err := json.Unmarshal([]byte(frame.data), &event); err != nil {
						t.Fatal(err)
					}
					if event.Content != nil {
						var text strings.Builder
						for _, p := range event.Content.Parts {
							text.WriteString(p.Text)
						}
						if strings.Contains(text.String(), "REVIEWER_ONLY") || strings.Contains(text.String(), "couldn't verify everything") {
							t.Fatalf("reviewer/generic appendix leaked: %q", text.String())
						}
						if event.InvocationID == reportID {
							if text.String() != blockedPublicationText {
								t.Fatalf("Home modified employee explanation: %q", text.String())
							}
							return true
						}
						if strings.Contains(text.String(), blockedPublicationText) {
							t.Fatalf("duplicate employee draft outside report: %s", event.ID)
						}
					}
					return false
				}
				var published blockedPublicationFrame
				for {
					frame := readBlockedPublicationFrame(t, rd)
					if check(frame) {
						published = frame
						break
					}
				}
				live.Body.Close()
				publisher := &bot.Publisher{Store: f.repo.(store.BotPublicationStore)}
				for range 2 {
					if _, err := publisher.Publish(f.ctx, f.b.ID, run.ID); err != nil {
						t.Fatal(err)
					}
				}
				if err := f.worker.Poll(context.Background()); err != nil {
					t.Fatal(err)
				}
				for range 2 {
					replay := open("")
					rr := bufio.NewReader(replay.Body)
					reports := 0
					for {
						frame := readBlockedPublicationFrame(t, rr)
						if frame.kind == "replay_complete" {
							break
						}
						if check(frame) {
							reports++
							if frame.id != published.id || frame.data != published.data {
								t.Fatal("live/reload event identity or payload changed")
							}
						}
					}
					replay.Body.Close()
					if reports != 1 {
						t.Fatalf("Home report count after publication retry: %d", reports)
					}
					history := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200))
					if strings.Count(history, blockedPublicationText) != 1 || strings.Contains(history, "REVIEWER_ONLY") || strings.Contains(history, "couldn't verify everything") {
						t.Fatalf("history changed/duplicated employee message: %s", history)
					}
				}
				resumed := open(published.id)
				rr := bufio.NewReader(resumed.Body)
				for {
					frame := readBlockedPublicationFrame(t, rr)
					if frame.kind == "replay_complete" {
						break
					}
					if check(frame) {
						t.Fatal("reconnect cursor replayed employee message")
					}
				}
				resumed.Body.Close()
				var restored store.BotRun
				if err := json.Unmarshal(f.request("GET", f.path+"/runs/"+run.ID.String(), "", 200), &restored); err != nil {
					t.Fatal(err)
				}
				if restored.Status != store.RunPartial || restored.Report.Summary != blockedPublicationText || bot.RenderReport(restored) != blockedPublicationText+"\n" {
					t.Fatalf("HTTP reload/terminal lost exact report: %+v", restored)
				}
				if llm.preparations != 1 || llm.productions != 1 || llm.reviews != 1 || f.pool.calls != 0 {
					t.Fatalf("unexpected repair/narration/tool replay: prepare=%d production=%d reviews=%d tools=%d", llm.preparations, llm.productions, llm.reviews, f.pool.calls)
				}
				t.Logf("%s: HTTP -> trusted unavailable transport -> production=1 repairs=0 -> partial -> Home live/reload exactly once; terminal text unchanged", scope)
			})
		}
	}
}
