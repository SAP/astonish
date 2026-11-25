package api

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

type workEvidenceProbe struct{ reads int }

func (p *workEvidenceProbe) ReadBotTranscript(context.Context, uuid.UUID, int64, int) ([]store.BotTranscriptEvent, error) {
	p.reads++
	return nil, errors.New("unexpected full transcript read")
}

type workEvidenceAuthorization struct {
	calls int
	err   error
}

func (a *workEvidenceAuthorization) Authorize(context.Context, execution.Capability) error {
	a.calls++
	return a.err
}
func (a *workEvidenceAuthorization) AuthorizeTool(context.Context, string) error { return a.err }

func TestBotWorkLoadOperationEndsAndRechecks(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	w := &progressOperationWork{work: store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})}
	s := &botAgentWorkService{req: req, resources: &BotChatResources{Work: w}}
	if _, err := s.load(ctx); err != nil {
		t.Fatal(err)
	}
	w.err = store.ErrBotForbidden
	if _, err := s.load(ctx); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal(err)
	}
	if len(w.seen) != 2 {
		t.Fatal("work authority cached")
	}
	for _, c := range w.seen {
		if entstore.WithBotContentReadOperationFromContext(context.Background(), c) != context.Background() {
			t.Fatal("operation survived load")
		}
	}
}

type workOperationSaveProbe struct {
	progressOperationWork
	saved context.Context
}

func (w *workOperationSaveProbe) SaveWork(ctx context.Context, q store.BotWorkUpdate) (*store.BotWorkState, error) {
	w.saved = ctx
	if entstore.WithBotContentReadOperationFromContext(context.Background(), ctx) == context.Background() {
		return nil, errors.New("operation ended before work commit")
	}
	return q.State.Clone(), nil
}

func TestBotWorkUpdateOperationEndsAfterCommit(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	w := &workOperationSaveProbe{progressOperationWork: progressOperationWork{work: store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})}}
	s := &botAgentWorkService{req: req, resources: &BotChatResources{Work: w}}
	if _, err := s.Update(ctx, bot.BotWorkChangeRequest{CallID: "notes", ExpectedRevision: w.work.Revision, Kind: bot.BotWorkReplaceNotes, Notes: "retained"}); err != nil {
		t.Fatal(err)
	}
	if w.saved == nil || entstore.WithBotContentReadOperationFromContext(context.Background(), w.saved) != context.Background() {
		t.Fatal("operation survived work commit")
	}
}

func TestBotWorkReadOperationBorrowsAndEnds(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	w := &progressOperationWork{work: store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})}
	s := &botAgentWorkService{req: req, resources: &BotChatResources{Work: w}}
	var seen context.Context
	s.resources.PriorRun = func(c context.Context) (*bot.BotPriorRunContext, error) {
		seen = c
		if entstore.WithBotContentReadOperationFromContext(context.Background(), c) == context.Background() {
			t.Fatal("read operation ended before nested reads")
		}
		return nil, nil
	}
	if _, err := s.Read(ctx, bot.BotWorkReadRequest{}); err != nil {
		t.Fatal(err)
	}
	if entstore.WithBotContentReadOperationFromContext(context.Background(), seen) != context.Background() {
		t.Fatal("read operation survived return")
	}
	outer, end := entstore.WithBotContentReadOperation(ctx)
	defer end()
	if _, err := s.Read(outer, bot.BotWorkReadRequest{}); err != nil {
		t.Fatal(err)
	}
	if entstore.WithBotContentReadOperationFromContext(context.Background(), seen) == context.Background() {
		t.Fatal("nested read closed outer operation")
	}
	end()
	if entstore.WithBotContentReadOperationFromContext(context.Background(), seen) != context.Background() {
		t.Fatal("nested read survived owner end")
	}
}

func TestBotWorkEvidenceUsesPersistedCurrentSession(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	auth := &workEvidenceAuthorization{}
	req.Authorization = auth
	base := session.InMemoryService()
	created, err := base.Create(ctx, &session.CreateRequest{AppName: "work", UserID: req.Bot.OwnerID.String(), SessionID: req.Run.SessionID.String()})
	if err != nil {
		t.Fatal(err)
	}
	sessions := &botProgressSessions{Service: base, current: created.Session, app: "work", user: req.Bot.OwnerID.String(), id: req.Run.SessionID.String()}
	event := &session.Event{ID: "current-response", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "read", Name: "read_file", Response: map[string]any{"content": "verified"}}}}}}}
	if err := sessions.AppendEvent(ctx, created.Session, event); err != nil {
		t.Fatal(err)
	}
	probe := &workEvidenceProbe{}
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})
	service := &botAgentWorkService{req: req, resources: &BotChatResources{Work: &agentExecutionWorkStore{state: w}, SessionService: sessions, EvidenceReader: probe}}
	for i := 0; i < 2; i++ {
		got, err := service.Evidence(ctx, bot.BotEvidenceReadRequest{CallID: "read"})
		if err != nil || got.Reference.EventID != event.ID {
			t.Fatalf("evidence=%+v err=%v", got, err)
		}
	}
	if probe.reads != 0 || auth.calls != 2 {
		t.Fatalf("full reads=%d auth=%d", probe.reads, auth.calls)
	}
	auth.err = store.ErrBotForbidden
	if _, err := service.Evidence(ctx, bot.BotEvidenceReadRequest{CallID: "read"}); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("revocation lost: %v", err)
	}
}
