package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestBotTitleConfirmedLaterQuestionContinuation(t *testing.T) {
	f := newInteractiveHTTPFixture(t, "ask_user", map[string]any{
		"kind": "select", "prompt": "Which Boston weather assignment?",
		"options":             []map[string]any{{"id": "forecast", "label": "Compare Boston forecasts"}, {"id": "climate", "label": "Explain Boston climate"}, {"id": "outing", "label": "Plan a Boston outing"}},
		"allow_custom_answer": true,
	}, []string{"chat", "tool:execute"}, store.BotScopePersonal)
	defer f.worker.Stop()
	var receipt titleHTTPReceipt
	if err := json.Unmarshal(f.request("POST", "/api/studio/bots", `{"name":"Boston weather","idempotency_key":"later-question","start_from_title":true}`, 201), &receipt); err != nil || receipt.InitialTurn == nil {
		t.Fatalf("admission: %v", err)
	}
	f.b, f.path = receipt.Bot, "/api/studio/bots/"+receipt.ID.String()
	f.request("PATCH", "/api/studio/sessions/"+f.b.HomeSessionID.String()+"/model", `{"provider":"controlled","model":"interactive-http"}`, 200)
	poll := func() *store.BotRun {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); ; {
			if err := f.worker.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			run, err := f.repo.GetRun(f.ctx, f.b.ID, receipt.InitialTurn.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if run.Status != store.RunQueued || time.Now().After(deadline) {
				return run
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	answer := func(run *store.BotRun, value string) uuid.UUID {
		t.Helper()
		if run.Status != store.RunWaitingApproval {
			t.Fatalf("expected question: %+v", run)
		}
		p, _ := execution.PrincipalFromContext(f.ctx)
		org, err := f.db.ForOrg(p.OrgSlug)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := org.ForUser(p.Subject).Sessions().(store.BotTranscriptReader).ReadBotTranscript(f.ctx, f.b.HomeSessionID, 0, 200)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Event != nil && row.Event.InvocationID == "bot-report:"+run.ID.String() {
				t.Fatalf("question-only wait appended a Home report: %+v", row.Event.Content)
			}
		}
		approvals, err := f.repo.ListApprovals(f.ctx, f.b.ID, store.BotPage{})
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range approvals {
			if a.Status != store.ApprovalPending {
				continue
			}
			body, _ := json.Marshal(map[string]any{"action_digest": a.ActionDigest, "response": map[string]any{"question_id": a.Interaction.QuestionID, "answer": value}})
			f.request("POST", f.path+"/approvals/"+a.ID.String()+"/approve-and-continue", string(body), 202)
			return a.ID
		}
		t.Fatal("no pending question")
		return uuid.Nil
	}
	const preference = "Celsius, please"
	initial := answer(poll(), preference)
	p, _ := execution.PrincipalFromContext(f.ctx)
	internal, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
	if err != nil {
		t.Fatal(err)
	}
	if err := entstore.RegisterBotHomeBinding(internal, f.repo, entstore.BotHomeRegistration{BotID: f.b.ID, ExpectedRevision: f.b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: f.b.ID, Scope: f.scope, Backend: "controlled", StorageKey: "journey-home", Revision: 1}}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
		t.Fatal(err)
	}
	// A preference is intentionally not a standalone task. Inspect the real
	// producer request after the launcher factory resumes the confirmed run,
	// rather than treating LoadRunInput's assignment alone as context evidence.
	producerRequests := 0
	assertPreferenceContext := func(req *model.LLMRequest) {
		t.Helper()
		raw, err := json.Marshal(req.Contents)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Boston weather", "Which Boston weather assignment?", preference} {
			if !strings.Contains(string(raw), want) {
				t.Fatalf("ordinary producer lost %q from confirmed preference context: %s", want, raw)
			}
		}
		producerRequests++
	}
	f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
		assertPreferenceContext(req)
		return nil, false
	}
	f.model.called, f.model.name, f.model.args = false, "ask_user", map[string]any{"kind": "text", "prompt": "Which forecast dates?"}
	later := answer(poll(), "Next weekend")
	if later == initial {
		t.Fatal("initial question replayed")
	}
	input, err := f.repo.(store.BotRuntimeStore).LoadRunInput(f.ctx, f.b.ID, receipt.InitialTurn.Run.ID)
	if err != nil || input.Instructions != preference {
		t.Fatalf("assignment lost: %+v %v", input, err)
	}
	f.model.called, f.model.name, f.model.args = false, "read_file", map[string]any{"path": "/isolated/receipt.txt"}
	const acknowledgment = "I'll compare the forecasts for next weekend."
	announced := false
	f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
		assertPreferenceContext(req)
		raw, _ := json.Marshal(req.Contents)
		for _, want := range []string{"Which forecast dates?", "Next weekend"} {
			if !strings.Contains(string(raw), want) {
				t.Fatalf("continued producer lost follow-up context %q: %s", want, raw)
			}
		}
		if announced {
			return nil, false
		}
		announced = true
		f.model.called = true
		return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: acknowledgment}, {FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: "read_file", Args: f.model.args}}}}}, true
	}
	completed := poll()
	if completed.Status != store.RunSucceeded || f.pool.calls != 1 || completed.TitleConfirmation.ApprovalID != initial {
		t.Fatalf("later answer did not continue once: %+v tool calls=%d", completed, f.pool.calls)
	}
	org, err := f.db.ForOrg(p.OrgSlug)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := org.ForUser(p.Subject).Sessions().(store.BotTranscriptReader).ReadBotTranscript(f.ctx, completed.SessionID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	seenAnswer, seenAck, seenTool := false, false, false
	for _, row := range rows {
		if row.Event == nil || row.Event.Content == nil {
			continue
		}
		for _, part := range row.Event.Content.Parts {
			if part == nil {
				continue
			}
			if row.Event.Content.Role == "user" && strings.Contains(part.Text, "Next weekend") {
				seenAnswer = true
			}
			if part.Text == acknowledgment {
				if !seenAnswer || seenTool {
					t.Fatal("acknowledgment not between answer and work")
				}
				seenAck = true
			}
			if part.FunctionCall != nil && part.FunctionCall.Name == "read_file" {
				if !seenAck {
					t.Fatal("work preceded friendly acknowledgment")
				}
				seenTool = true
			}
		}
	}
	if !seenTool {
		t.Fatal("Home missing ordered work call")
	}
	if completed.TitleConfirmation.ConfirmedAssignment != preference {
		t.Fatal("continuation changed confirmed assignment")
	}
	if producerRequests < 3 {
		t.Fatalf("checked %d producer requests, want confirmation, continuation and post-tool synthesis", producerRequests)
	}
	t.Logf("confirmed short preference retained Boston weather title and original question in %d actual producer requests, including later question/answer and post-tool synthesis", producerRequests)
}
