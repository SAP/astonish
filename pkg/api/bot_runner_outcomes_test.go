package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"iter"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

type agentExecutionWorkStore struct {
	state *store.BotWorkState
	err   error
}

func (s *agentExecutionWorkStore) LoadWork(context.Context, store.BotLease) (*store.BotWorkState, error) {
	return s.state.Clone(), nil
}
func (s *agentExecutionWorkStore) SaveWork(_ context.Context, q store.BotWorkUpdate) (*store.BotWorkState, error) {
	if s.err != nil {
		return nil, s.err
	}
	if err := store.ValidateBotWorkUpdate(s.state, &q.State); err != nil {
		return nil, err
	}
	s.state = q.State.Clone()
	s.state.Revision++
	return s.state.Clone(), nil
}

type agentEvidenceReader struct{ rows []store.BotTranscriptEvent }

func (r agentEvidenceReader) ReadBotTranscript(_ context.Context, _ uuid.UUID, cursor int64, limit int) ([]store.BotTranscriptEvent, error) {
	var out []store.BotTranscriptEvent
	for _, row := range r.rows {
		if row.Cursor > cursor && len(out) < limit {
			out = append(out, row)
		}
	}
	return out, nil
}

type agentCheckModel struct {
	run func(context.Context, *model.LLMRequest) iter.Seq2[*model.LLMResponse, error]
}

func (*agentCheckModel) Name() string { return "check-fixture" }
func (m *agentCheckModel) GenerateContent(ctx context.Context, q *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return m.run(ctx, q)
}

func TestBotAgentAccessFlushedBeforeModelAndRetainedOnFailure(t *testing.T) {
	for _, status := range []int{200, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})
			saved := &agentExecutionWorkStore{state: w}
			response := map[string]any{"status": status, "content": "footer only; no requested facts"}
			raw, _ := json.Marshal(response)
			event := &session.Event{ID: "source-event", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "source", Name: "web_fetch", Response: response}}}}}}
			service := &botAgentWorkService{req: req, resources: &BotChatResources{Work: saved, EvidenceReader: agentEvidenceReader{rows: []store.BotTranscriptEvent{{Cursor: 1, Event: event}}}}}
			fact := store.BotSourceAccessFact{EvidenceRef: "capture:source", CallID: "source", OriginalURL: "https://example.test/source", HTTPStatus: status, Status: store.BotSourceAccessReturned, ObservedAt: time.Now().UTC(), BodyDigest: botAgentDigest(raw)}
			if status == 429 {
				fact.Status = store.BotSourceAccessFailed
			}
			service.captureAccess("source", []store.BotSourceAccessFact{fact})
			ctx = bot.WithBotWorkServices(ctx, service)
			called := false
			wrapped := &botAgentReadModel{journal: &botAgentReadJournal{}, LLM: &agentCheckModel{run: func(context.Context, *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
				return func(yield func(*model.LLMResponse, error) bool) {
					called = true
					if len(saved.state.AgentWork.SourceAccess) != 1 || saved.state.AgentWork.SourceAccess[0] != fact {
						t.Error("source not durable at next model boundary")
					}
					yield(nil, errors.New("provider unavailable"))
				}
			}}}
			for range wrapped.GenerateContent(ctx, &model.LLMRequest{}, false) {
			}
			if !called || len(saved.state.AgentWork.Evidence) != 1 {
				t.Fatal("missing durable source")
			}
			// A restart reads the same negative/transport fact, without certifying its usefulness.
			service = &botAgentWorkService{req: req, resources: service.resources}
			read, err := service.Evidence(ctx, bot.BotEvidenceReadRequest{CallID: "source", Limit: 8})
			if err != nil || !read.Truncated || read.TotalBytes != len(raw) {
				t.Fatalf("bounded evidence: %+v %v", read, err)
			}
			bad := fact
			bad.BodyDigest = strings.Repeat("0", 64)
			service.captureAccess("source", []store.BotSourceAccessFact{bad})
			if _, err := service.Read(ctx, bot.BotWorkReadRequest{}); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("digest mismatch accepted: %v", err)
			}
			if len(saved.state.AgentWork.SourceAccess) != 1 {
				t.Fatal("bad digest overwrote source")
			}
		})
	}
}

func TestBotAgentCheckConcurrentUpdateUsageAndCancellation(t *testing.T) {
	for _, cancelCheck := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelCheck), func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})
			saved := &agentExecutionWorkStore{state: w}
			service := &botAgentWorkService{req: req, resources: &BotChatResources{Work: saved}}
			service.llm = &agentCheckModel{run: func(context.Context, *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
				return func(yield func(*model.LLMResponse, error) bool) {
					// A synchronous model boundary may reenter read/update; holding the service mutex deadlocks.
					if _, err := service.Update(ctx, bot.BotWorkChangeRequest{CallID: "during-check", ExpectedRevision: 2, Kind: bot.BotWorkReplaceNotes, Notes: "retained concurrent update"}); err != nil {
						t.Error(err)
					}
					if cancelCheck {
						cancel()
					}
					response := &model.LLMResponse{Content: genai.NewContentFromText("Unsupported claim; consult another source.", "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 11, CandidatesTokenCount: 7}}
					yield(response, nil)
					yield(response, nil)
				}
			}}
			q := bot.BotWorkCheckRequest{CallID: "check", Question: "Is the draft supported?", DraftText: "Draft"}
			receipt, err := service.Check(ctx, q)
			if service.usage.InputTokens != 11 || service.usage.OutputTokens != 7 {
				t.Fatalf("double/lost usage: %+v", service.usage)
			}
			if saved.state.AgentWork.Notes != "retained concurrent update" {
				t.Fatal("clobbered concurrent update")
			}
			if cancelCheck {
				if !errors.Is(err, context.Canceled) || len(saved.state.AgentWork.Checks) != 0 {
					t.Fatalf("late mutation: %+v %v", receipt, err)
				}
				freshCtx, _, _ := botChatTestRequest(t)
				service = &botAgentWorkService{req: req, resources: service.resources}
				if _, err := service.Check(freshCtx, q); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("interrupted check replayed: %v", err)
				}
			} else if err != nil || receipt.Status != store.BotWorkCheckReturned || saved.state.Revision != 4 {
				t.Fatalf("check: %+v %v", receipt, err)
			}
		})
	}
}

func TestBotAgentUnfinishedItemsDoNotGateAnswer(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})
	w.AgentWork.Items = []store.BotWorkItem{{ID: uuid.New(), Summary: "More investigation", Status: store.BotWorkItemInProgress}}
	saved := &agentExecutionWorkStore{state: w}
	result, next, err := runBotAgentWork(ctx, req, &BotChatResources{Work: saved}, w, func(_ bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
		c.FinalEventID = "final"
		return bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: "Useful partial answer"}}, nil
	})
	if err != nil || result.Status != store.RunPartial || result.Report.Summary != "Useful partial answer" || next.AgentWork.Items[0].Status != store.BotWorkItemPending || next.Review != nil {
		t.Fatalf("answer gated: %+v %v", result, err)
	}
}

func TestBotAgentCheckMalformedAndOversizedFeedbackRecover(t *testing.T) {
	for _, text := range []string{"", strings.Repeat("x", 8193)} {
		t.Run(fmt.Sprint(len(text)), func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})
			saved := &agentExecutionWorkStore{state: w}
			service := &botAgentWorkService{req: req, resources: &BotChatResources{Work: saved}, llm: NewMockLLM(TextTurn(text))}
			receipt, err := service.Check(ctx, bot.BotWorkCheckRequest{CallID: "malformed", Question: "Check draft"})
			if err != nil || receipt.Status != store.BotWorkCheckFailed || receipt.Judgment != store.BotWorkCheckUnverified {
				t.Fatalf("malformed: %+v %v", receipt, err)
			}
			service.llm = NewMockLLM(TextTurn("The revised draft states its limitations."))
			receipt, err = service.Check(ctx, bot.BotWorkCheckRequest{CallID: "corrected", Question: "Check revised draft"})
			if err != nil || receipt.Status != store.BotWorkCheckReturned || len(saved.state.AgentWork.Checks) != 2 || saved.state.Phase != store.BotWorkWorking {
				t.Fatalf("recovery: %+v %v", receipt, err)
			}
		})
	}
}

func TestBotAgentPreviousAndContinuationAreScoped(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})
	saved := &agentExecutionWorkStore{state: w}
	prior := w.Clone()
	prior.Contract.RunID = uuid.New()
	projection := store.BuildBotWorkProjection(w)
	projection.RunID = prior.Contract.RunID
	resources := &BotChatResources{Work: saved, PreviousWork: func(context.Context) (*store.BotWorkProjection, error) { return projection, nil }}
	service := &botAgentWorkService{req: req, resources: resources}
	read, err := service.Read(ctx, bot.BotWorkReadRequest{})
	if err != nil || read.Previous == nil || read.Previous.RunID != projection.RunID {
		t.Fatalf("previous: %+v %v", read, err)
	}
	read.Previous.Items = append(read.Previous.Items, store.BotWorkItem{})
	if len(projection.Items) != 0 {
		t.Fatal("previous aliases callback storage")
	}
	projection.BotID = uuid.New()
	if _, err := service.Read(ctx, bot.BotWorkReadRequest{}); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("foreign previous accepted: %v", err)
	}
	id := uuid.New()
	q := bot.BotWorkChangeRequest{CallID: "remaining", ExpectedRevision: 1, Kind: bot.BotWorkSetRemaining, Remaining: []store.BotWorkObligation{{Summary: "Remaining", Reason: "Scheduled", Continuation: store.BotWorkContinueExistingSchedule, ContinuationID: &id}}}
	if _, err := service.Update(ctx, q); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("unbound continuation: %v", err)
	}
	resources.ValidateWorkContinuation = func(_ context.Context, kind store.BotWorkContinuationKind, got uuid.UUID) error {
		if kind != store.BotWorkContinueExistingSchedule || got != id {
			t.Fatal("wrong callback identity")
		}
		return store.ErrBotForbidden
	}
	if _, err := service.Update(ctx, q); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("denied continuation: %v", err)
	}
	resources.ValidateWorkContinuation = func(context.Context, store.BotWorkContinuationKind, uuid.UUID) error { return nil }
	if _, err := service.Update(ctx, q); err != nil {
		t.Fatal(err)
	}
}

func TestBotAgentWorkUpdateRedactsBeforePersistence(t *testing.T) {
	const secret = "progress-secret-123456789"
	for _, redact := range []bool{true, false} {
		t.Run(fmt.Sprintf("redactor=%t", redact), func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})
			saved := &agentExecutionWorkStore{state: w}
			redactor := credentials.NewRedactor()
			redactor.AddSecret("progress-test", secret)
			event := &session.Event{ID: "source-event", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "source", Name: "read_file", Response: map[string]any{"content": "plain evidence"}}}}}}}
			resources := &BotChatResources{Work: saved, EvidenceReader: agentEvidenceReader{rows: []store.BotTranscriptEvent{{Cursor: 1, Event: event}}}}
			if redact {
				resources.Redact = redactor.Redact
			}
			service := &botAgentWorkService{req: req, resources: resources}
			itemID := uuid.NewSHA1(req.Run.ID, []byte("work-item:item"))
			continuationID := uuid.New()
			resources.ValidateWorkContinuation = func(_ context.Context, kind store.BotWorkContinuationKind, id uuid.UUID) error {
				if kind != store.BotWorkContinueExistingSchedule || id != continuationID {
					t.Fatal("continuation identity changed")
				}
				return nil
			}
			requests := []bot.BotWorkChangeRequest{
				{CallID: "item", Kind: bot.BotWorkUpsertItem, Item: &store.BotWorkItem{Summary: "Investigate " + secret, Reason: "Access used " + secret, Status: store.BotWorkItemInProgress, EvidenceRefs: []string{"capture:source"}}},
				{CallID: "plain-item", Kind: bot.BotWorkUpsertItem, Item: &store.BotWorkItem{Summary: "Plain summary", Reason: "Plain reason", Status: store.BotWorkItemPending}},
				{CallID: "remaining", Kind: bot.BotWorkSetRemaining, Remaining: []store.BotWorkObligation{
					{Summary: "Follow up " + secret, Reason: "Waiting for " + secret, WorkItemID: &itemID, Continuation: store.BotWorkContinueExistingSchedule, ContinuationID: &continuationID},
					{Summary: "Plain remaining", Reason: "Plain blocker", Continuation: store.BotWorkContinueOwnerRequest},
				}},
				{CallID: "notes", Kind: bot.BotWorkReplaceNotes, Notes: "Private notes " + secret},
				{CallID: "cancel", Kind: bot.BotWorkCancelItem, Item: &store.BotWorkItem{ID: itemID}},
			}
			for _, q := range requests {
				q.ExpectedRevision = saved.state.Revision
				projection, err := service.Update(ctx, q)
				if err != nil || projection == nil {
					t.Fatalf("%s: projection=%+v err=%v", q.CallID, projection, err)
				}
				for name, value := range map[string]any{"saved state": saved.state, "returned projection": projection} {
					raw, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(raw), secret) == redact {
						t.Fatalf("%s %s: unexpected secret visibility", q.CallID, name)
					}
				}
				// Retries keep the digest of the original request, not rewritten text.
				if _, err := service.Update(ctx, q); err != nil || saved.state.Revision != projection.WorkRevision {
					t.Fatalf("%s: retry changed state: %v", q.CallID, err)
				}
			}
			agentWork := saved.state.AgentWork
			item := agentWork.Items[0]
			if item.ID != itemID || item.Status != store.BotWorkItemCancelled || len(item.EvidenceRefs) != 1 || item.EvidenceRefs[0] != "capture:source" || agentWork.Evidence[0].CallID != "source" {
				t.Fatal("item identity, status or evidence changed")
			}
			remaining := agentWork.Remaining[0]
			if remaining.ID != uuid.NewSHA1(req.Run.ID, []byte("remaining:remaining:0")) || remaining.SourceRunID != req.Run.ID || remaining.WorkItemID == nil || *remaining.WorkItemID != itemID || remaining.ContinuationID == nil || *remaining.ContinuationID != continuationID {
				t.Fatal("remaining obligation identities changed")
			}
			if agentWork.Items[1].Summary != "Plain summary" || agentWork.Items[1].Reason != "Plain reason" || agentWork.Remaining[1].Summary != "Plain remaining" || agentWork.Remaining[1].Reason != "Plain blocker" {
				t.Fatal("plain text changed")
			}
			if redact && (item.Summary != redactor.Redact(requests[0].Item.Summary) || item.Reason != redactor.Redact(requests[0].Item.Reason) || remaining.Summary != redactor.Redact(requests[2].Remaining[0].Summary) || remaining.Reason != redactor.Redact(requests[2].Remaining[0].Reason) || agentWork.Notes != redactor.Redact(requests[3].Notes)) {
				t.Fatal("text not passed through scoped redactor")
			}
			if !strings.Contains(requests[0].Item.Summary, secret) || !strings.Contains(requests[2].Remaining[0].Summary, secret) {
				t.Fatal("redaction mutated caller input")
			}
		})
	}
}

func TestBotAgentWorkServiceRevisionAndReplay(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})
	saved := &agentExecutionWorkStore{state: w}
	service := &botAgentWorkService{req: req, resources: &BotChatResources{Work: saved}}
	change := bot.BotWorkChangeRequest{ExpectedRevision: 1, CallID: "item-create", Kind: bot.BotWorkUpsertItem, Item: &store.BotWorkItem{Summary: "Read source", Status: store.BotWorkItemInProgress}}
	first, err := service.Update(ctx, change)
	if err != nil || first.WorkRevision != 2 || len(first.Items) != 1 {
		t.Fatalf("create: %+v %v", first, err)
	}
	id := first.Items[0].ID
	first.Items[0].Summary = "tampered caller copy"
	service = &botAgentWorkService{req: req, resources: &BotChatResources{Work: saved}}
	replay, err := service.Update(ctx, change)
	if err != nil || replay.WorkRevision != 2 || replay.Items[0].Summary != "Read source" {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	change.Notes = "different request"
	if _, err := service.Update(ctx, change); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("changed replay: %v", err)
	}
	change.CallID = "stale"
	current, err := service.Update(ctx, change)
	if !errors.Is(err, store.ErrBotConflict) || current.WorkRevision != 2 {
		t.Fatalf("stale overwrite: %+v %v", current, err)
	}
	change = bot.BotWorkChangeRequest{ExpectedRevision: 2, CallID: "item-complete", Kind: bot.BotWorkUpsertItem, Item: &store.BotWorkItem{ID: id, Summary: "Read source", Status: store.BotWorkItemCompleted}}
	if _, err := service.Update(ctx, change); err != nil {
		t.Fatal(err)
	}
	change.ExpectedRevision = 3
	change.CallID = "foreign-item"
	change.Item.ID[0] ^= 1
	if _, err := service.Update(ctx, change); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("foreign item: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := service.Read(cancelled, bot.BotWorkReadRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
}

func TestBotAgentCheckReplayCannotChangeQuestion(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})
	saved := &agentExecutionWorkStore{state: w}
	service := &botAgentWorkService{req: req, resources: &BotChatResources{Work: saved}}
	q := bot.BotWorkCheckRequest{CallID: "check", Question: "Is this supported?", DraftText: "Draft"}
	first, err := service.Check(ctx, q)
	if err != nil || first.Status != store.BotWorkCheckFailed {
		t.Fatalf("unavailable check: %+v %v", first, err)
	}
	service = &botAgentWorkService{req: req, resources: &BotChatResources{Work: saved}} // process-local state is gone
	if _, err := service.Check(ctx, q); err != nil || saved.state.Revision != 3 {
		t.Fatalf("check replay changed state: %v", err)
	}
	q.Question = "Different question"
	if _, err := service.Check(ctx, q); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("changed check accepted: %v", err)
	}
}

func TestBotAgentCaptureFailureRetainsEvidence(t *testing.T) {
	for _, cancelAfter := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelAfter), func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})
			s := &agentExecutionWorkStore{state: w, err: errors.New("capture unavailable")}
			calls := 0
			result, _, err := runBotAgentWork(ctx, req, &BotChatResources{Work: s}, w, func(_ bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
				calls++
				c.FinalEventID = "final"
				c.FinalText = "Draft must not publish"
				if cancelAfter {
					cancel()
				}
				return bot.RunResult{Status: store.RunSucceeded, Usage: store.BotUsage{ToolCalls: 1, InputTokens: 7, OutputTokens: 3}, Report: store.BotReport{Summary: c.FinalText, Unchanged: []store.BotEvidence{{Summary: "Observed source"}}}}, nil
			})
			if err == nil || calls != 1 || result.Usage.ToolCalls != 1 || result.Usage.InputTokens != 7 || len(result.Report.Unchanged) != 1 || result.Report.Summary == "Draft must not publish" || result.Report.Work == nil {
				t.Fatalf("lost execution facts: %+v %v", result, err)
			}
			if cancelAfter && result.Status != store.RunInterrupted {
				t.Fatal("cancel not interrupted")
			}
		})
	}
}

func TestBotAgentWorkDispositions(t *testing.T) {
	for _, d := range []store.AgentWorkDisposition{store.AgentWorkAnswered, store.AgentWorkComplete, store.AgentWorkPartial, store.AgentWorkBlocked} {
		t.Run(string(d), func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})
			w.AgentWork.Disposition = d
			s := &agentExecutionWorkStore{state: w}
			result, saved, err := runBotAgentWork(ctx, req, &BotChatResources{Work: s}, w, func(_ bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
				c.FinalEventID = "final"
				return bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: "Useful result"}}, nil
			})
			if err != nil || saved.Review != nil || result.Report.Work.Disposition != d || result.Report.Summary != "Useful result" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			want := store.RunSucceeded
			if d == store.AgentWorkPartial || d == store.AgentWorkBlocked {
				want = store.RunPartial
			}
			if result.Status != want {
				t.Fatalf("status %s", result.Status)
			}
		})
	}
}

func TestBotChatRuntimeAdapterToolOutcomeCannotBecomeSuccess(t *testing.T) {
	cases := []struct {
		name   string
		output map[string]any
		class  bot.RunErrorClass
	}{
		{"denied", map[string]any{"state": string(agent.ProgressiveStateAuthorizationDenied)}, bot.ErrorBlocked},
		{"approval", map[string]any{"state": agent.ProgressiveStateApprovalRequired}, bot.ErrorBlocked},
		{"execution", map[string]any{"state": agent.ProgressiveStateExecutionError}, bot.ErrorPermanent},
		{"context_required", map[string]any{"state": string(agent.ProgressiveStateContextRequired)}, bot.ErrorPermanent},
		{"context_stale", map[string]any{"state": agent.ProgressiveStateContextStale}, bot.ErrorPermanent},
		{"error", map[string]any{"error": "fixture failure"}, bot.ErrorPermanent},
		{"policy_like_text", map[string]any{"error": "bot policy: exact action requires approval"}, bot.ErrorPermanent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, req, ss := botChatTestRequest(t)
			// These cases exercise returned result states, not authorization. The
			// trusted fixture runtime lets the native guard reach the fixture.
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botChatTestClassifier{}, Guard: botChatTestGuard{}})
			var cleaned, executed atomic.Int64
			fixture, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Return a controlled outcome"}, func(tool.Context, map[string]any) (map[string]any, error) {
				executed.Add(1)
				return tc.output, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			llm := NewMockLLM(ToolCallTurn("read_file", map[string]any{}), TextTurn("Everything succeeded."))
			adapter, err := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, llm, []tool.Tool{fixture}, &cleaned))
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if err == nil || result.Status == store.RunSucceeded || result.Failure == nil || result.Failure.Class != tc.class {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if cleaned.Load() != 1 || executed.Load() != 1 || result.Usage.ToolCalls != 1 {
				t.Fatalf("cleanup=%d executed=%d usage=%+v", cleaned.Load(), executed.Load(), result.Usage)
			}
		})
	}
}

func TestBotChatRuntimeAdapterNativePolicyFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool string
		kind execution.PolicyDecisionKind
	}{
		{"approval", "read_file", execution.PolicyRequestApproval},
		{"denied", "write_file", execution.PolicyDeny},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No trusted fixture classifier: exercise native enforcement before
			// the tool executes. The model's subsequent claim cannot erase it.
			ctx, req, ss := botChatTestRequest(t)
			var cleaned, executed atomic.Int64
			fixture, err := functiontool.New(functiontool.Config{Name: tc.tool, Description: "Must not execute"}, func(tool.Context, map[string]any) (map[string]any, error) {
				executed.Add(1)
				return map[string]any{"content": "unexpected effect"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			llm := NewMockLLM(ToolCallTurn(tc.tool, map[string]any{}), TextTurn("Everything succeeded."))
			adapter, err := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, llm, []tool.Tool{fixture}, &cleaned))
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if err == nil || result.Status == store.RunSucceeded || result.Failure == nil || result.Failure.Class != bot.ErrorBlocked {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			var policyErr *agent.BotPolicyError
			if !errors.As(err, &policyErr) || policyErr.Decision.Kind != tc.kind || policyErr.ApprovalRequired() != (tc.kind == execution.PolicyRequestApproval) {
				t.Fatalf("lost policy decision %s: %v", tc.kind, err)
			}
			if cleaned.Load() != 1 || executed.Load() != 0 || result.Usage.ToolCalls != 1 {
				t.Fatalf("cleanup=%d executed=%d usage=%+v", cleaned.Load(), executed.Load(), result.Usage)
			}
		})
	}
}

func TestBotAgentTrustedReadFailureAlternateAttempt(t *testing.T) {
	ctx, req, ss := botChatTestRequest(t)
	req.Work = &store.BotWorkState{Protocol: store.BotWorkProtocolAgentLedV1}
	ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
	ctx = execution.WithRunPolicy(ctx, req.Policy)
	ctx = context.WithValue(ctx, botChatBudgetKey{}, req.Budget)
	ctx, _ = bot.WithToolOutcomes(ctx)
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botChatTestClassifier{}, Guard: botChatTestGuard{}})
	var paths []string
	action, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read controlled sources"}, func(c tool.Context, args map[string]any) (map[string]any, error) {
		if err := agent.AuthorizeBotTool(c, "read_file", args); err != nil {
			return nil, err
		}
		path, _ := args["path"].(string)
		paths = append(paths, path)
		response := map[string]any{"content": "alternate facts"}
		status := store.BotSourceAccessReturned
		if path == "first" {
			response = map[string]any{"error": "source unavailable", "state": string(agent.ProgressiveStateExecutionError)}
			status = store.BotSourceAccessFailed
		}
		raw, err := json.Marshal(response)
		if err != nil {
			return nil, err
		}
		bot.RecordToolOutcome(c, c.FunctionCallID(), bot.ToolOutcome{SourceAccess: []store.BotSourceAccessFact{{CallID: c.FunctionCallID(), EvidenceRef: "capture:" + c.FunctionCallID(), OriginalURL: "https://example.test/" + path, Status: status, ObservedAt: time.Now().UTC(), BodyDigest: botAgentDigest(raw)}}})
		return response, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	llm := NewMockLLM(ToolCallTurn("read_file", map[string]any{"path": "first"}), ToolCallTurn("read_file", map[string]any{"path": "alternate"}), TextTurn("Answer from alternate facts; first source unavailable."))
	var cleaned atomic.Int64
	resources, err := botChatTestFactory(t, ss, llm, []tool.Tool{action}, &cleaned)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	existing, err := ss.Get(ctx, &session.GetRequest{AppName: studioChatAppName, UserID: req.Bot.OwnerID.String(), SessionID: req.Run.SessionID.String()})
	if err != nil {
		t.Fatal(err)
	}
	var sequence int64
	var completion ChatCompletion
	result, err := runBotChatAttempt(ctx, req, resources, func(context.Context, bot.RuntimeEvent) error { return nil }, nil, existing, nil, false, &sequence, &completion)
	if err != nil || result.Status != store.RunSucceeded || result.Report.Summary != "Answer from alternate facts; first source unavailable." || len(result.Report.Unverified) != 0 {
		t.Fatalf("recovered answer rejected: %+v %v", result, err)
	}
	if strings.Join(paths, ",") != "first,alternate" || len(llm.Calls) != 3 || completion.Usage.ToolCalls != 2 || completion.toolResponses != 2 {
		t.Fatalf("replayed/lost execution: paths=%v calls=%d completion=%+v", paths, len(llm.Calls), completion)
	}
	persisted, err := ss.Get(ctx, &session.GetRequest{AppName: studioChatAppName, UserID: req.Bot.OwnerID.String(), SessionID: req.Run.SessionID.String()})
	if err != nil {
		t.Fatal(err)
	}
	retainedFailure := false
	for event := range persisted.Session.Events().All() {
		if event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part != nil && part.FunctionResponse != nil && part.FunctionResponse.Response["error"] == "source unavailable" {
				retainedFailure = true
			}
		}
	}
	if !retainedFailure {
		t.Fatal("failed access disappeared from transcript")
	}
}

func TestBotAgentTrustedReadFailureCompletionRecovery(t *testing.T) {
	for _, mode := range []string{"recover", "execution_state", "execution_state_only", "permission", "approval", "context_required", "context_stale", "native_policy", "native_cancel", "native_budget", "cancelled", "write", "send", "changed_effect", "artifact", "untrusted", "wrong_call", "wrong_body", "wrong_name", "returned_fact", "legacy", "ordinary_chat", "replayed"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botChatTestClassifier{}, Guard: botChatTestGuard{}})
			ctx, _ = bot.WithToolOutcomes(ctx)
			journal := &botAgentReadJournal{}
			if mode != "legacy" {
				ctx = context.WithValue(ctx, botAgentReadJournalKey{}, journal)
			}
			if mode == "ordinary_chat" {
				ctx, _ = bot.WithToolOutcomes(context.Background())
				ctx = context.WithValue(ctx, botAgentReadJournalKey{}, journal)
			}
			cr := newChatRunner("session", "user", studioChatAppName, false)
			defer cr.cancel()
			cr.ctx = ctx
			if mode == "cancelled" {
				var cancel context.CancelFunc
				cr.ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			response := map[string]any{"error": "source unavailable", "status": 429}
			switch mode {
			case "execution_state":
				response["state"] = agent.ProgressiveStateExecutionError
			case "execution_state_only":
				response["state"] = string(agent.ProgressiveStateExecutionError)
				delete(response, "error")
			case "permission":
				response["state"] = agent.ProgressiveStateAuthorizationDenied
			case "approval":
				response["state"] = agent.ProgressiveStateApprovalRequired
			case "context_required":
				response["state"] = agent.ProgressiveStateContextRequired
			case "context_stale":
				response["state"] = agent.ProgressiveStateContextStale
			case "native_policy":
				response["error"] = &agent.BotPolicyError{Decision: execution.PolicyDecision{Kind: execution.PolicyDeny}}
			case "native_cancel":
				response["error"] = fmt.Errorf("cancelled: %w", context.Canceled)
			case "native_budget":
				response["error"] = errors.New("shared budget exhausted")
			}
			name := "web_fetch"
			if mode == "write" || mode == "send" {
				name = mode + "_file"
				effect := execution.EffectReversibleWrite
				if mode == "send" {
					effect = execution.EffectSend
				}
				ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botReportTestClassifier{effect: effect, verified: true}})
			}
			call := &genai.FunctionCall{ID: "failed", Name: name, Args: map[string]any{"url": "https://example.test/failed"}}
			journal.capture(ctx, call)
			cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}})
			raw, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			fact := store.BotSourceAccessFact{EvidenceRef: "capture:failed", CallID: call.ID, OriginalURL: "https://example.test/failed", HTTPStatus: 429, Status: store.BotSourceAccessFailed, ObservedAt: time.Now().UTC(), BodyDigest: botAgentDigest(raw)}
			switch mode {
			case "wrong_call":
				fact.CallID = "other"
			case "wrong_body":
				fact.BodyDigest = strings.Repeat("0", 64)
			case "returned_fact":
				fact.Status = store.BotSourceAccessReturned
			}
			outcome := bot.ToolOutcome{SourceAccess: []store.BotSourceAccessFact{fact}}
			if mode == "changed_effect" {
				outcome.Changed = []store.BotEvidence{{Summary: "uncertain write"}}
			}
			if mode == "artifact" {
				outcome.Artifacts = []bot.ToolReportArtifact{{}}
			}
			if mode != "untrusted" {
				bot.RecordToolOutcome(cr.ctx, call.ID, outcome)
			} else {
				response[bot.ToolOutcomeKey] = outcome
			}
			responseName := name
			if mode == "wrong_name" {
				responseName = "other_tool"
			}
			event := &session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: call.ID, Name: responseName, Response: response}}}}}}
			cr.observeCompletion(event)
			if mode == "replayed" {
				cr.observeCompletion(event)
			}
			// The agent chooses a different source, rather than retrying the failed access.
			alternate := &genai.FunctionCall{ID: "alternate", Name: "web_fetch", Args: map[string]any{"url": "https://example.test/alternate"}}
			journal.capture(ctx, alternate)
			cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: alternate}}}}})
			bot.RecordToolOutcome(cr.ctx, alternate.ID, bot.ToolOutcome{Verified: true})
			cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: alternate.ID, Name: alternate.Name, Response: map[string]any{"content": "alternate source facts"}}}}}}})
			cr.observeCompletion(&session.Event{ID: "final", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Answer from alternate source; first source unavailable.", "model")}})
			completion := cr.Completion()
			wantRecovery := mode == "recover" || mode == "execution_state" || mode == "execution_state_only"
			if got := completion.Err == nil && !completion.Blocked; got != wantRecovery {
				t.Fatalf("recovery=%v want=%v completion=%+v", got, wantRecovery, completion)
			}
			if !completion.Final || completion.FinalEventID != "final" || completion.Usage.ToolCalls != 2 {
				t.Fatalf("lost final answer/accounting: %+v", completion)
			}
			if cause, ok := response["error"].(error); ok && !errors.Is(completion.Err, cause) {
				t.Fatalf("lost native error identity: %v", completion.Err)
			}
		})
	}
}

func TestChatCompletionToolError(t *testing.T) {
	policyErr := &agent.BotPolicyError{Decision: execution.PolicyDecision{Kind: execution.PolicyRequestApproval, Reason: "fixture approval"}}
	ordinaryErr := errors.New("ordinary tool failure")
	for _, tc := range []struct {
		name   string
		value  any
		cause  error
		policy bool
	}{
		{"typed_policy", policyErr, policyErr, true},
		{"wrapped_policy", fmt.Errorf("wrapped: %w", policyErr), policyErr, true},
		{"ordinary_error", ordinaryErr, ordinaryErr, false},
		{"policy_like_text", policyErr.Error(), nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cr := newChatRunner("session", "user", studioChatAppName, false)
			defer cr.cancel()
			cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "fixture", Response: map[string]any{"error": tc.value}}}}}}})
			cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Everything succeeded.", "model")}})
			completion := cr.Completion()
			if completion.Err == nil || (tc.cause != nil && !errors.Is(completion.Err, tc.cause)) {
				t.Fatalf("lost tool failure: %+v", completion)
			}
			var got *agent.BotPolicyError
			if errors.As(completion.Err, &got) != tc.policy {
				t.Fatalf("incorrect policy classification: %v", completion.Err)
			}
		})
	}
}

func TestBotChatRuntimeAdapterTypedCompletionAccounting(t *testing.T) {
	cr := newChatRunner("session", "user", studioChatAppName, false)
	defer cr.cancel()
	cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Partial: true, Content: genai.NewContentFromText("partial is not final", "model")}})
	if cr.Completion().Final {
		t.Fatal("partial became final")
	}
	cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("observed answer", "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 12, CandidatesTokenCount: 3}}})
	completion := cr.Completion()
	if !completion.Final || completion.FinalText != "observed answer" || completion.Usage.InputTokens != 12 || completion.Usage.OutputTokens != 3 {
		t.Fatalf("completion=%+v", completion)
	}
	cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "read_file"}}}}}})
	if cr.Completion().Final {
		t.Fatal("earlier answer masked subsequent unfinished tool call")
	}
}

func TestBotChatRuntimeAdapterFactoryFailureCleansResources(t *testing.T) {
	for _, mode := range []string{"error", "invalid", "cleanup_panic"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, ss := botChatTestRequest(t)
			var cleaned atomic.Int64
			adapter, err := NewBotChatRuntimeAdapter(func(ctx context.Context, req bot.RunRequest) (*BotChatResources, error) {
				cleanup := func() { cleaned.Add(1) }
				switch mode {
				case "error":
					return &BotChatResources{Cleanup: cleanup}, errors.New("construction failed")
				case "invalid":
					return &BotChatResources{Cleanup: cleanup}, nil
				default:
					r, err := botChatTestFactory(t, ss, NewMockLLM(TextTurn("answer")), nil, &cleaned)(ctx, req)
					r.Cleanup = func() { cleanup(); panic("cleanup failed") }
					return r, err
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if err == nil || result.Status == store.RunSucceeded || cleaned.Load() != 1 {
				t.Fatalf("result=%+v err=%v cleanup=%d", result, err, cleaned.Load())
			}
		})
	}
}
