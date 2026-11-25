package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
)

type titleHTTPReceipt struct {
	store.Bot
	InitialTurn *store.BotMessageReceipt `json:"initial_turn"`
}

func titleStates(t *testing.T, f *interactiveHTTPFixture, b store.Bot) ([]store.BotRunState, int) {
	t.Helper()
	raw := f.request("GET", "/api/studio/bots/"+b.ID.String()+"/sessions/"+b.HomeSessionID.String()+"/stream", "", 200)
	var states []store.BotRunState
	users := 0
	kind := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "event: ") {
			kind = strings.TrimPrefix(line, "event: ")
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := []byte(strings.TrimPrefix(line, "data: "))
		if kind == "user_message" {
			users++
		}
		if kind == store.BotRunStateKey {
			var state store.BotRunState
			if err := json.Unmarshal(payload, &state); err != nil {
				t.Fatal(err)
			}
			if state.SchemaVersion != 1 || state.BotID != b.ID || state.HomeSessionID != b.HomeSessionID {
				t.Fatalf("invalid state envelope: %s", payload)
			}
			if len(states) > 0 && state.StateSequence <= states[len(states)-1].StateSequence {
				t.Fatal("non-monotonic state")
			}
			states = append(states, state)
		}
	}
	return states, users
}

func TestBotTitleStartHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "read_file", map[string]any{"path": "/isolated/receipt.txt"}, []string{"chat", "tool:execute"}, scope)
			body := `{"name":"Research the exact title","idempotency_key":"title-acceptance","start_from_title":true}`
			var first, again titleHTTPReceipt
			if err := json.Unmarshal(f.request("POST", "/api/studio/bots", body, 201), &first); err != nil {
				t.Fatal(err)
			}
			if first.InitialTurn == nil || first.InitialTurn.Run.Status != store.RunQueued || !first.InitialTurn.Run.IsInteractiveChat() || first.Status != store.BotDraft {
				t.Fatalf("not atomically queued: %+v", first)
			}
			if first.InitialTurn.UserEventID == "" || first.InitialTurn.HistoryCursor < 1 || first.HomeSessionID != first.InitialTurn.HomeSessionID {
				t.Fatal("missing canonical receipt")
			}
			if f.model.called {
				t.Fatal("provider ran during creation")
			}
			input, err := f.repo.(store.BotRuntimeStore).LoadRunInput(f.ctx, first.ID, first.InitialTurn.Run.ID)
			if err != nil || input.Instructions != "Research the exact title" || !strings.Contains(input.Version.Charter, input.Instructions) || len(input.Version.Autonomy.AllowedTools) != 0 {
				t.Fatalf("saved title/defaults: %+v %v", input, err)
			}
			if err = json.Unmarshal(f.request("POST", "/api/studio/bots", body, 201), &again); err != nil {
				t.Fatal(err)
			}
			if again.ID != first.ID || again.InitialTurn.Run.ID != first.InitialTurn.Run.ID || again.InitialTurn.UserEventID != first.InitialTurn.UserEventID {
				t.Fatal("retry duplicated title")
			}
			runs, err := f.repo.ListRuns(f.ctx, first.ID, store.BotPage{})
			if err != nil || len(runs) != 1 {
				t.Fatalf("runs: %v %v", runs, err)
			}
			states, users := titleStates(t, f, first.Bot)
			if users != 1 || len(states) != 1 || states[0].Status != store.RunQueued {
				t.Fatalf("initial transcript users=%d states=%+v", users, states)
			}
			// Legacy create and GET remain inert even when the interactive service is ready.
			f.request("GET", f.path, "", 200)
			f.request("GET", f.path+"/snapshot", "", 200)
			legacy, err := f.repo.ListRuns(f.ctx, f.b.ID, store.BotPage{})
			if err != nil || len(legacy) != 0 {
				t.Fatalf("GET initialized legacy draft: %v %v", legacy, err)
			}
			f.request("POST", "/api/studio/bots", strings.Replace(body, "Research the exact title", "Changed title", 1), 409)
		})
	}
}

func TestBotTitleConfirmationStateHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, mode := range []string{"option", "custom"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				f := newInteractiveHTTPFixture(t, "ask_user", map[string]any{
					"kind": "select", "prompt": "What should I research about Boston weather?",
					"options":             []map[string]any{{"id": "forecast", "label": "Compare Boston forecasts"}, {"id": "climate", "label": "Explain Boston seasonal weather"}, {"id": "outing", "label": "Plan a Boston outing"}},
					"allow_custom_answer": true,
				}, []string{"chat", "tool:execute"}, scope)
				defer f.worker.Stop()
				var receipt titleHTTPReceipt
				if err := json.Unmarshal(f.request("POST", "/api/studio/bots", `{"name":"Boston weather","idempotency_key":"confirm-title","start_from_title":true}`, 201), &receipt); err != nil || receipt.InitialTurn == nil {
					t.Fatalf("title admission: %v %+v", err, receipt)
				}
				f.b, f.path = receipt.Bot, "/api/studio/bots/"+receipt.ID.String()
				f.request("PATCH", "/api/studio/sessions/"+f.b.HomeSessionID.String()+"/model", `{"provider":"controlled","model":"interactive-http"}`, 200)
				if err := f.worker.Poll(context.Background()); err != nil {
					t.Fatal(err)
				}
				run, err := f.repo.GetRun(f.ctx, f.b.ID, receipt.InitialTurn.Run.ID)
				if err != nil || run == nil {
					t.Fatalf("load title run: %v", err)
				}
				if run.Status != store.RunWaitingApproval || run.TitleConfirmation == nil || run.TitleConfirmation.State != store.BotTitleConfirmationWaiting {
					t.Fatalf("title did not ask before execution: status=%s confirmation=%+v", run.Status, run.TitleConfirmation)
				}
				// A waiting report describes the owner question; it is not a final
				// publication. Assert the pending approval and absence of work facts.
				if f.pool.calls != 0 || run.Task != nil || run.Report.Work != nil || len(run.Report.Artifacts) != 0 || len(run.Report.NeedsApproval) != 1 {
					t.Fatalf("research or final publication before confirmation: pool=%d report=%+v task=%+v", f.pool.calls, run.Report, run.Task)
				}
				approvals, err := f.repo.ListApprovals(f.ctx, f.b.ID, store.BotPage{})
				if err != nil || len(approvals) != 1 || store.ValidateBotTitleQuestion(approvals[0].Interaction) != nil {
					t.Fatalf("title question: %+v %v", approvals, err)
				}
				a := approvals[0]
				states, users := titleStates(t, f, f.b)
				if users != 1 || len(states) != 3 || states[2].Status != store.RunWaitingApproval {
					t.Fatalf("question-first states: %+v users=%d", states, users)
				}
				for reload := 0; reload < 2; reload++ {
					var snapshot store.BotSnapshot
					if err := json.Unmarshal(f.request("GET", f.path+"/snapshot", "", 200), &snapshot); err != nil {
						t.Fatal(err)
					}
					if len(snapshot.PendingApprovals) != 1 || !reflect.DeepEqual(snapshot.PendingApprovals[0].Interaction, a.Interaction) || snapshot.PendingApprovals[0].ID != a.ID {
						t.Fatalf("reload replaced question: %+v", snapshot.PendingApprovals)
					}
					if err := f.worker.Poll(context.Background()); err != nil {
						t.Fatal(err)
					}
					reloaded, _ := titleStates(t, f, f.b)
					if !reflect.DeepEqual(states, reloaded) || f.pool.calls != 0 {
						t.Fatal("reload or waiting poll started execution")
					}
				}
				answer, assignment := "forecast", "Compare Boston forecasts"
				if mode == "custom" {
					answer, assignment = "Compare weekend rain in Boston", "Compare weekend rain in Boston"
				}
				body, _ := json.Marshal(map[string]any{"action_digest": a.ActionDigest, "response": map[string]any{"question_id": a.Interaction.QuestionID, "answer": answer}})
				answerPath := f.path + "/approvals/" + a.ID.String() + "/approve-and-continue"
				f.request("POST", answerPath, string(body), 202)
				f.request("POST", answerPath, string(body), 202) // retry cannot create another run
				input, err := f.repo.(store.BotRuntimeStore).LoadRunInput(f.ctx, f.b.ID, run.ID)
				if err != nil || input.Instructions != assignment {
					t.Fatalf("confirmed assignment not loaded: %+v %v", input, err)
				}
				confirmed, err := f.repo.GetRun(f.ctx, f.b.ID, run.ID)
				if err != nil || confirmed.TitleConfirmation == nil || confirmed.TitleConfirmation.State != store.BotTitleConfirmationConfirmed || confirmed.TitleConfirmation.OriginalTitle != "Boston weather" || confirmed.UserEventID != receipt.InitialTurn.UserEventID {
					t.Fatalf("confirmation lost original title binding: %+v %v", confirmed, err)
				}
				// Only now install the controlled Home binding needed by ordinary
				// execution. This is not physical-container absence evidence.
				p, _ := execution.PrincipalFromContext(f.ctx)
				internal, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
				if err != nil {
					t.Fatal(err)
				}
				if err := entstore.RegisterBotHomeBinding(internal, f.repo, entstore.BotHomeRegistration{BotID: f.b.ID, ExpectedRevision: f.b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: f.b.ID, Scope: f.scope, Backend: "controlled", StorageKey: "journey-home", Revision: 1}}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
					t.Fatal(err)
				}
				f.model.called, f.model.name, f.model.args = false, "read_file", map[string]any{"path": "/isolated/receipt.txt"}
				if err := f.worker.Poll(context.Background()); err != nil {
					t.Fatal(err)
				}
				completed, err := f.repo.GetRun(f.ctx, f.b.ID, run.ID)
				if err != nil || completed.Status != store.RunSucceeded || completed.Report.Summary == "" || f.pool.calls != 1 {
					t.Fatalf("confirmed execution: %+v %v tool calls=%d", completed, err, f.pool.calls)
				}
				if f.model.reviews != 0 {
					t.Fatal("read-only title execution invoked mandatory end review")
				}
				history := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200))
				publications := 0
				for _, line := range strings.Split(history, "\n") {
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					var event struct {
						InvocationID string `json:"invocationId"`
						Text         string `json:"text"`
					}
					if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
						t.Fatal(err)
					}
					if event.InvocationID == "bot-report:"+run.ID.String() && completed.Report.EmployeeMessage != nil && event.Text == completed.Report.EmployeeMessage.Text {
						publications++
					}
				}
				if publications != 1 {
					t.Fatalf("final publication count=%d", publications)
				}
			})
		}
	}
}

func TestBotRunStateHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "ask_user", map[string]any{"kind": "text", "prompt": "Which report?"}, []string{"chat", "tool:execute"}, scope)
			var receipt store.BotMessageReceipt
			if err := json.Unmarshal(f.request("POST", f.path+"/messages", fmt.Sprintf(`{"version":%d,"instructions":"Ask then finish","idempotency_key":"state-turn"}`, f.b.Version), 202), &receipt); err != nil {
				t.Fatal(err)
			}
			assertStates := func(want []store.RunStatus) {
				t.Helper()
				states, _ := titleStates(t, f, f.b)
				got := make([]store.RunStatus, len(states))
				for i, s := range states {
					got[i] = s.Status
					if s.RunID != receipt.Run.ID {
						t.Fatal("wrong run state")
					}
				}
				if !reflect.DeepEqual(got, want) {
					projection, _ := json.Marshal(states)
					t.Fatalf("states=%v want=%v projections=%s", got, want, projection)
				}
				// Repeated reload must not append or initialize anything.
				reloaded, _ := titleStates(t, f, f.b)
				if !reflect.DeepEqual(states, reloaded) {
					t.Fatal("reload mutated states")
				}
				var snap store.BotSnapshot
				if err := json.Unmarshal(f.request("GET", f.path+"/snapshot", "", 200), &snap); err != nil {
					t.Fatal(err)
				}
				if len(snap.RunStates) != 1 || snap.RunStates[0].Status != want[len(want)-1] || snap.RunStates[0].StateSequence < states[len(states)-1].StateSequence || snap.HistoryCursor < receipt.HistoryCursor {
					t.Fatalf("snapshot diverged: %+v", snap)
				}
			}
			assertStates([]store.RunStatus{store.RunQueued})
			if err := f.worker.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertStates([]store.RunStatus{store.RunQueued, store.RunRunning, store.RunRunning, store.RunWaitingApproval})
			// Claim commits seq=3 without Work; SaveWork commits seq=5 with
			// agent-led working revision 2 before approval commits seq=6.
			// These are distinct durable projections, not duplicate delivery.
			states, _ := titleStates(t, f, f.b)
			if states[1].StateSequence != 3 || states[1].Work != nil || states[2].StateSequence != 5 || states[2].Work == nil || states[2].Work.Protocol != store.BotWorkProtocolAgentLedV1 || states[2].Work.WorkRevision != 2 || states[3].StateSequence != 6 {
				t.Fatalf("claim/work/approval projection mismatch: %+v", states)
			}
			approvals, err := f.repo.ListApprovals(f.ctx, f.b.ID, store.BotPage{})
			if err != nil || len(approvals) != 1 || approvals[0].Interaction == nil {
				t.Fatalf("approval: %v %v", approvals, err)
			}
			a := approvals[0]
			body, _ := json.Marshal(map[string]any{"action_digest": a.ActionDigest, "response": map[string]any{"question_id": a.Interaction.QuestionID, "answer": "Quarterly report"}})
			f.request("POST", f.path+"/approvals/"+a.ID.String()+"/approve-and-continue", string(body), 202)
			assertStates([]store.RunStatus{store.RunQueued, store.RunRunning, store.RunRunning, store.RunWaitingApproval, store.RunQueued})
			if err = f.worker.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertStates([]store.RunStatus{store.RunQueued, store.RunRunning, store.RunRunning, store.RunWaitingApproval, store.RunQueued, store.RunRunning, store.RunRunning, store.RunRunning, store.RunRunning, store.RunSucceeded})
			// The resumable live endpoint sends precisely persisted rows, then no
			// rows after reconnecting from its final session-qualified cursor.
			path := f.server.URL + f.path + "/sessions/" + f.b.HomeSessionID.String() + "/events?scope=" + string(f.scope)
			cursor := ""
			for pass := 0; pass < 2; pass++ {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				req, _ := http.NewRequestWithContext(ctx, "GET", path, nil)
				req.Header.Set("Last-Event-ID", cursor)
				res, err := f.server.Client().Do(req)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				if res.StatusCode != 200 {
					res.Body.Close()
					cancel()
					t.Fatal(res.Status)
				}
				rd := bufio.NewReader(res.Body)
				count := 0
				for {
					event := ""
					data := ""
					for {
						line, err := rd.ReadString('\n')
						if err != nil {
							t.Fatal(err)
						}
						line = strings.TrimSuffix(line, "\n")
						if line == "" {
							break
						}
						if strings.HasPrefix(line, "id: ") {
							cursor = strings.TrimPrefix(line, "id: ")
						}
						if strings.HasPrefix(line, "event: ") {
							event = strings.TrimPrefix(line, "event: ")
						}
						if strings.HasPrefix(line, "data: ") {
							data = strings.TrimPrefix(line, "data: ")
						}
					}
					if event == "replay_complete" {
						break
					}
					if strings.Contains(data, `"stateDelta"`) || event == "transcript" {
						t.Fatalf("legacy protocol %s: %s", event, data)
					}
					if event == store.BotRunStateKey {
						var state store.BotRunState
						if err := json.Unmarshal([]byte(data), &state); err != nil || state.BotID != f.b.ID || state.RunID != receipt.Run.ID {
							t.Fatalf("invalid canonical state: %s (%v)", data, err)
						}
						count++
					}
				}
				res.Body.Close()
				cancel()
				want := 10 // claim, committed work projection, approval/requeue, resumed work and terminal status
				if pass == 1 {
					want = 0
				}
				if count != want {
					t.Fatalf("reconnect state count=%d want=%d", count, want)
				}
			}
		})
	}
}
