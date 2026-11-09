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

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/session"
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
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var e session.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e); err != nil {
			t.Fatal(err)
		}
		if e.Author == "user" && e.Content != nil {
			users++
		}
		if value, ok := e.Actions.StateDelta[store.BotRunStateKey]; ok {
			payload, _ := json.Marshal(value)
			var state store.BotRunState
			if err := json.Unmarshal(payload, &state); err != nil {
				t.Fatal(err)
			}
			if e.Content != nil || state.SchemaVersion != 1 || state.BotID != b.ID || state.HomeSessionID != b.HomeSessionID {
				t.Fatalf("invalid state envelope: %+v", e)
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
					t.Fatalf("states=%v want=%v", got, want)
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
			assertStates([]store.RunStatus{store.RunQueued, store.RunRunning, store.RunWaitingApproval})
			approvals, err := f.repo.ListApprovals(f.ctx, f.b.ID, store.BotPage{})
			if err != nil || len(approvals) != 1 || approvals[0].Interaction == nil {
				t.Fatalf("approval: %v %v", approvals, err)
			}
			a := approvals[0]
			body, _ := json.Marshal(map[string]any{"action_digest": a.ActionDigest, "response": map[string]any{"question_id": a.Interaction.QuestionID, "answer": "Quarterly report"}})
			f.request("POST", f.path+"/approvals/"+a.ID.String()+"/approve-and-continue", string(body), 202)
			assertStates([]store.RunStatus{store.RunQueued, store.RunRunning, store.RunWaitingApproval, store.RunQueued})
			if err = f.worker.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertStates([]store.RunStatus{store.RunQueued, store.RunRunning, store.RunWaitingApproval, store.RunQueued, store.RunRunning, store.RunSucceeded})
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
					if event != "transcript" {
						t.Fatalf("unexpected protocol %s", event)
					}
					if strings.Contains(data, store.BotRunStateKey) {
						count++
					}
				}
				res.Body.Close()
				cancel()
				want := 6
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
