package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestBotPromptReadCurrentEvidenceAvoidsHistory(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	auth := &workEvidenceAuthorization{}
	req.Authorization = auth
	base := session.InMemoryService()
	created, err := base.Create(ctx, &session.CreateRequest{AppName: "work", UserID: req.Bot.OwnerID.String(), SessionID: req.Run.SessionID.String()})
	if err != nil {
		t.Fatal(err)
	}
	counted := &progressCountingSessions{Service: base}
	conversation := &botConversationSession{Session: created.Session}
	for i := 0; i < 16; i++ {
		conversation.prior = append(conversation.prior, &session.Event{ID: fmt.Sprintf("historical-%d", i), LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "old", Response: map[string]any{"old": true}}}}}}})
	}
	tracker := &botProgressSessions{Service: counted, current: conversation, app: "work", user: req.Bot.OwnerID.String(), id: req.Run.SessionID.String()}
	reader := &accessScanReader{}
	for i := 0; i < 25; i++ {
		e := &session.Event{ID: fmt.Sprintf("current-%d", i), LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: fmt.Sprintf("call-%d", i), Response: map[string]any{"n": i}}}}}}}
		if err := tracker.AppendEvent(ctx, created.Session, e); err != nil {
			t.Fatal(err)
		}
		reader.rows = append(reader.rows, store.BotTranscriptEvent{Cursor: int64(i + 1), Event: e})
	}
	tracker.current = conversation
	work := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})
	s := &botAgentWorkService{req: req, resources: &BotChatResources{Work: &agentExecutionWorkStore{state: work}, SessionService: tracker, EvidenceReader: reader}}
	check := func() {
		t.Helper()
		got, err := s.Read(ctx, bot.BotWorkReadRequest{Limit: 20, PromptSnapshot: true})
		if err != nil {
			t.Fatal(err)
		}
		if reader.reads != 0 || counted.gets != 0 {
			t.Fatalf("prompt reopened transcript history: reads=%d gets=%d", reader.reads, counted.gets)
		}
		if len(got.Evidence) != 20 || got.NextCursor != "" {
			t.Fatalf("unbounded snapshot: %+v", got)
		}
		for i, ref := range got.Evidence {
			raw, _ := json.Marshal(map[string]any{"n": i + 5})
			if ref.CallID != fmt.Sprintf("call-%d", i+5) || ref.RunID != req.Run.ID || ref.Digest != botAgentDigest(raw) {
				t.Fatalf("order/content changed: %+v", ref)
			}
		}
	}
	check()
	check()
	auth.err = store.ErrBotForbidden
	if _, err := s.Read(ctx, bot.BotWorkReadRequest{PromptSnapshot: true}); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("revocation lost: %v", err)
	}
	auth.err = nil
	got, err := s.Read(ctx, bot.BotWorkReadRequest{Limit: 20})
	if err != nil || reader.reads != 1 || got.Evidence[0].CallID != "call-0" || got.NextCursor != "20" {
		t.Fatalf("explicit pagination changed: %+v %v", got, err)
	}
	tracker.current = &botConversationSession{Session: botProgressSessionSnapshot{Session: created.Session, events: botConversationEvents{}}, prior: conversation.prior}
	got, err = s.Read(ctx, bot.BotWorkReadRequest{PromptSnapshot: true})
	if err != nil || len(got.Evidence) != 0 || reader.reads != 1 {
		t.Fatalf("prior Home history became current-run evidence: %+v %v", got, err)
	}
	tracker.current = nil
	if _, err := s.Read(ctx, bot.BotWorkReadRequest{PromptSnapshot: true}); err != nil || reader.reads != 2 {
		t.Fatalf("unavailable snapshot fallback: %v reads=%d", err, reader.reads)
	}
}

func TestBotPromptEvidenceSnapshotPersistenceAndOrdering(t *testing.T) {
	ctx := context.Background()
	base := session.InMemoryService()
	created, err := base.Create(ctx, &session.CreateRequest{AppName: "app", UserID: "owner", SessionID: "run"})
	if err != nil {
		t.Fatal(err)
	}
	counted := &progressCountingSessions{Service: base}
	s := &botProgressSessions{Service: counted, current: created.Session, app: "app", user: "owner", id: "run"}
	rows, available, err := s.promptEvidence(ctx, 20)
	if err != nil || !available || len(rows) != 0 || counted.gets != 0 {
		t.Fatalf("empty snapshot: %v %v %v", rows, available, err)
	}
	part := func(id string) *genai.Part {
		return &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: id, Response: map[string]any{"id": id}}}
	}
	e := &session.Event{ID: "persisted", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{part("a"), nil, part("b"), part("c")}}}}
	counted.fail = store.ErrBotConflict
	if err := s.AppendEvent(ctx, created.Session, e); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal(err)
	}
	rows, _, _ = s.promptEvidence(ctx, 20)
	if len(rows) != 0 {
		t.Fatal("failed append became evidence")
	}
	counted.fail = nil
	if err := s.AppendEvent(ctx, created.Session, e); err != nil {
		t.Fatal(err)
	}
	for _, ignored := range []*session.Event{
		{ID: "partial", LLMResponse: model.LLMResponse{Partial: true, Content: &genai.Content{Role: "tool", Parts: []*genai.Part{part("partial")}}}},
		{ID: "model", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part("model")}}}},
	} {
		if err := s.AppendEvent(ctx, created.Session, ignored); err != nil {
			t.Fatal(err)
		}
	}
	rows, available, err = s.promptEvidence(ctx, 2)
	if err != nil || !available || len(rows) != 2 || rows[0].Event.Content.Parts[0].FunctionResponse.ID != "b" || rows[1].Event.Content.Parts[0].FunctionResponse.ID != "c" {
		t.Fatalf("part order/filter/bound changed: %+v %v", rows, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := s.promptEvidence(cancelled, 20); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s.user = "foreign"
	if _, _, err := s.promptEvidence(ctx, 20); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal(err)
	}
}

func TestBotPromptReadDoesNotFlushPendingAccess(t *testing.T) {
	s, _, _, pending := accessScanFixture(t, 1, 1024)
	_, req, _ := botChatTestRequest(t)
	s.req.Budget = req.Budget
	s.accessPending = pending
	// Optional prompt reads must not spend their short timeout on a durable CAS.
	_, err := s.Read(context.Background(), bot.BotWorkReadRequest{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.accessPending) == 0 {
		t.Fatal("optional prompt read flushed pending access")
	}
	if _, err := s.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.accessPending) != 0 {
		t.Fatal("mandatory producer load did not flush access")
	}
}
